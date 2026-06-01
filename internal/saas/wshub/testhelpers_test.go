package wshub

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"quantlab/internal/saas/agentauth"
	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

// pipeConn is an in-memory Conn for tests. WriteFrame from the
// Connection side ends up in the test-readable clientReads channel;
// the test pushes frames into clientWrites which the Connection's
// ReadFrame returns.
type pipeConn struct {
	clientReads  chan []byte // server→client
	clientWrites chan []byte // client→server
	closed       atomic.Bool
	closeOnce    sync.Once
}

func newPipeConn() *pipeConn {
	return &pipeConn{
		clientReads:  make(chan []byte, 64),
		clientWrites: make(chan []byte, 64),
	}
}

func (p *pipeConn) ReadFrame(ctx context.Context) ([]byte, error) {
	if p.closed.Load() {
		return nil, ErrConnClosed
	}
	select {
	case frame, ok := <-p.clientWrites:
		if !ok {
			return nil, ErrConnClosed
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pipeConn) WriteFrame(ctx context.Context, frame []byte) error {
	if p.closed.Load() {
		return ErrConnClosed
	}
	cp := append([]byte(nil), frame...)
	select {
	case p.clientReads <- cp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pipeConn) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		close(p.clientReads)
		close(p.clientWrites)
	})
	return nil
}

// clientSend pushes one Agent-side frame.
func (p *pipeConn) clientSend(t *testing.T, frame []byte) {
	t.Helper()
	select {
	case p.clientWrites <- frame:
	case <-time.After(2 * time.Second):
		t.Fatalf("clientSend: blocked >2s")
	}
}

// clientReadEnv waits for one server-side frame and decodes its envelope.
func (p *pipeConn) clientReadEnv(t *testing.T) wire.Envelope {
	t.Helper()
	select {
	case frame, ok := <-p.clientReads:
		if !ok {
			t.Fatalf("clientReadEnv: conn closed")
		}
		env, err := wire.DecodeEnvelope(frame)
		if err != nil {
			t.Fatalf("clientReadEnv: decode envelope: %v", err)
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatalf("clientReadEnv: no frame in 2s")
		return wire.Envelope{}
	}
}

// --- fake TokenStore for agentauth.Service ---

type fakeTokenStore struct {
	mu   sync.Mutex
	rows map[string]*store.AgentToken
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{rows: make(map[string]*store.AgentToken)}
}

func (f *fakeTokenStore) GetByAgentID(_ context.Context, agentID string) (*store.AgentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[agentID]
	if !ok {
		return nil, nil
	}
	cp := *row
	return &cp, nil
}

func (f *fakeTokenStore) Create(_ context.Context, row *store.AgentToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *row
	f.rows[row.AgentID] = &cp
	return nil
}

func (f *fakeTokenStore) Revoke(_ context.Context, agentID string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	row, ok := f.rows[agentID]
	if !ok {
		return nil
	}
	if row.RevokedAt == nil {
		row.RevokedAt = &now
	}
	return nil
}

func (f *fakeTokenStore) TouchLastSeen(_ context.Context, agentID string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if row, ok := f.rows[agentID]; ok {
		row.LastSeenAt = &now
	}
	return nil
}

// authFixture wires a real agentauth.Service backed by a fakeTokenStore
// (bcrypt.MinCost for speed) and returns a freshly-issued plaintext
// token along with the AgentID and AccountID it binds.
func authFixture(t *testing.T) (*agentauth.Service, *fakeTokenStore, string, string, string) {
	t.Helper()
	fs := newFakeTokenStore()
	svc := agentauth.NewService(fs).WithBcryptCost(bcrypt.MinCost)
	created, err := svc.CreateToken(context.Background(), "01HKACCT00000000000000000A", "test-fixture")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}
	return svc, fs, created.Plaintext, created.AgentID, "01HKACCT00000000000000000A"
}

// --- deterministic clock ---

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	tickers []*fakeTicker
}

func newFakeClock(start time.Time) *fakeClock {
	return &fakeClock{now: start}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTicker(_ time.Duration) Ticker {
	t := &fakeTicker{ch: make(chan time.Time, 8)}
	c.mu.Lock()
	c.tickers = append(c.tickers, t)
	c.mu.Unlock()
	return t
}

// fire emits one tick to every live ticker.
func (c *fakeClock) fire() {
	c.mu.Lock()
	now := c.now
	tt := append([]*fakeTicker(nil), c.tickers...)
	c.mu.Unlock()
	for _, t := range tt {
		if t.stopped.Load() {
			continue
		}
		select {
		case t.ch <- now:
		default:
		}
	}
}

type fakeTicker struct {
	ch      chan time.Time
	stopped atomic.Bool
}

func (t *fakeTicker) C() <-chan time.Time { return t.ch }
func (t *fakeTicker) Stop()               { t.stopped.Store(true) }

// --- misc test helpers ---

func encodeForClient(t *testing.T, msgType wire.MessageType, accountID string, payload any) []byte {
	t.Helper()
	raw, err := wire.EncodeMessage(msgType, "01HKMSG00000000000000000AB", time.Now().UnixMilli(), accountID, payload)
	if err != nil {
		t.Fatalf("encode %s: %v", msgType, err)
	}
	return raw
}

// stableMsgID returns a counter-based ULID-shaped msg generator so test
// assertions are stable across runs.
func stableMsgID() func() string {
	var n atomic.Int32
	return func() string {
		v := n.Add(1)
		// 26-char Crockford-ish; not a real ULID but passes envelope
		// length checks (codec only requires non-empty msg_id).
		s := []byte("01HKTEST00000000000000000")
		// overwrite last byte with counter mod alphabet
		alpha := "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford
		s = append(s, alpha[int(v)%len(alpha)])
		return string(s)
	}
}

var _ = jsonRaw // silence unused warning if test doesn't use

func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
