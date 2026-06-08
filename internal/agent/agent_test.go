package agent

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/wire"
	"quantlab/internal/wsconn"
)

// pipeConn is the test-only Conn — a channel pair. Same shape as the
// wshub fake but redeclared here to avoid the test sub-package import.
type pipeConn struct {
	serverReads  chan []byte // server (Agent) reads
	serverWrites chan []byte // server (Agent) writes; client reads
	closed       atomic.Bool
	closeOnce    sync.Once
}

func newPipeConn() *pipeConn {
	return &pipeConn{
		serverReads:  make(chan []byte, 64),
		serverWrites: make(chan []byte, 64),
	}
}

func (p *pipeConn) ReadFrame(ctx context.Context) ([]byte, error) {
	if p.closed.Load() {
		return nil, wsconn.ErrConnClosed
	}
	select {
	case frame, ok := <-p.serverReads:
		if !ok {
			return nil, wsconn.ErrConnClosed
		}
		return frame, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (p *pipeConn) WriteFrame(ctx context.Context, frame []byte) error {
	if p.closed.Load() {
		return wsconn.ErrConnClosed
	}
	cp := append([]byte(nil), frame...)
	select {
	case p.serverWrites <- cp:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *pipeConn) Close() error {
	p.closeOnce.Do(func() {
		p.closed.Store(true)
		close(p.serverReads)
		close(p.serverWrites)
	})
	return nil
}

// hubSimulator drives the SaaS side of the WS protocol within a test.
// pushes frames to serverReads (Agent receives), reads frames the Agent
// pushed to serverWrites.
func (p *pipeConn) hubSendEnv(t *testing.T, msgType wire.MessageType, accountID string, payload any) string {
	t.Helper()
	msgID := "01HKTSTSAAS000000000000000" + string(byte('A')+byte(time.Now().UnixNano()&0xf))
	raw, err := wire.EncodeMessage(msgType, msgID, time.Now().UnixMilli(), accountID, payload)
	if err != nil {
		t.Fatalf("encode %s: %v", msgType, err)
	}
	select {
	case p.serverReads <- raw:
	case <-time.After(2 * time.Second):
		t.Fatalf("hubSendEnv: blocked >2s")
	}
	return msgID
}

func (p *pipeConn) hubReadEnv(t *testing.T) wire.Envelope {
	t.Helper()
	select {
	case frame, ok := <-p.serverWrites:
		if !ok {
			t.Fatalf("hubReadEnv: conn closed")
		}
		env, err := wire.DecodeEnvelope(frame)
		if err != nil {
			t.Fatalf("hubReadEnv: decode envelope: %v", err)
		}
		return env
	case <-time.After(2 * time.Second):
		t.Fatalf("hubReadEnv: no frame in 2s")
		return wire.Envelope{}
	}
}

// staticDialer returns a pre-built pipeConn from Dial. nthDial counts
// invocations so reconnect tests can verify backoff retries.
type staticDialer struct {
	mu      sync.Mutex
	conns   []*pipeConn
	dialErr error
	dialN   atomic.Int32
}

func (s *staticDialer) Dial(_ context.Context, _ string, _ http.Header) (wsconn.Conn, error) {
	s.dialN.Add(1)
	if s.dialErr != nil {
		return nil, s.dialErr
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.conns) == 0 {
		return nil, errors.New("staticDialer: no more conns")
	}
	c := s.conns[0]
	s.conns = s.conns[1:]
	return c, nil
}

// newTestClient wires a Client with mockable knobs. cfg is minimal but
// valid; the test-injected dialer returns the supplied pipeConn(s).
func newTestClient(t *testing.T, conns []*pipeConn, ex Exchange) *Client {
	t.Helper()
	cfg := Config{
		AgentID:   "01HKAGENT00000000000000000",
		AccountID: "01HKACCT00000000000000000A",
		SaaSURL:   "ws://test/api/v1/ws/agent",
		SaaSToken: "agt_01HKAGENT00000000000000000_FAKESECRET",
		Exchange:  ExchangeConfig{Name: "mock"},
	}
	dialer := &staticDialer{conns: conns}
	idem := NewMemoryStore()
	c := NewClient(cfg, ex, idem, Options{
		Dialer:        dialer,
		Backoff:       []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		BackoffJitter: func() float64 { return 1.0 }, // deterministic
		NowFn:         func() time.Time { return time.Unix(1700000000, 0) },
	})
	return c
}

// drives the SaaS side handshake against the Agent client.
func runHubHandshake(t *testing.T, pc *pipeConn, accountID string) (helloEnv, authEnv, syncEnv wire.Envelope) {
	t.Helper()
	helloEnv = pc.hubReadEnv(t)
	if helloEnv.Type != wire.TypeHello {
		t.Fatalf("expected hello, got %q", helloEnv.Type)
	}
	pc.hubSendEnv(t, wire.TypeAuthRequired, accountID, wire.AuthRequired{})
	authEnv = pc.hubReadEnv(t)
	if authEnv.Type != wire.TypeAuth {
		t.Fatalf("expected auth, got %q", authEnv.Type)
	}
	pc.hubSendEnv(t, wire.TypeAuthOK, accountID, wire.AuthOK{
		ServerNowMs: time.Now().UnixMilli(),
		AgentID:     "01HKAGENT00000000000000000",
	})
	pc.hubSendEnv(t, wire.TypeStateSyncRequest, accountID, wire.StateSyncRequest{})
	syncEnv = pc.hubReadEnv(t)
	if syncEnv.Type != wire.TypeStateSyncResponse {
		t.Fatalf("expected state_sync_response, got %q", syncEnv.Type)
	}
	return
}

func runClientInBg(t *testing.T, c *Client) (context.CancelFunc, chan error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() { errCh <- c.Run(ctx) }()
	return cancel, errCh
}

// ----- Tests -----

func TestHandshake_HappyPath(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(65000)})
	ex.SetPosition(Position{Symbol: "USDT", Free: decimal.NewFromInt(10000), Locked: decimal.Zero})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	hello, auth, sync := runHubHandshake(t, pc, c.cfg.AccountID)

	h, _ := wire.DecodePayload[wire.Hello](hello)
	if h.AccountID != c.cfg.AccountID {
		t.Errorf("hello.account_id = %q, want %q", h.AccountID, c.cfg.AccountID)
	}
	if h.SchemaVersion != wire.SchemaVersion {
		t.Errorf("hello.schema_version = %q", h.SchemaVersion)
	}
	a, _ := wire.DecodePayload[wire.Auth](auth)
	if a.Token != c.cfg.SaaSToken {
		t.Errorf("auth.token = %q, want %q", a.Token, c.cfg.SaaSToken)
	}
	s, _ := wire.DecodePayload[wire.StateSyncResponse](sync)
	if len(s.Positions) != 1 || s.Positions[0].Symbol != "USDT" {
		t.Errorf("state_sync.positions = %+v", s.Positions)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

func TestHandshake_AuthFailIsFatal(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(nil)
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	// Drive hub side: auth_fail mid-handshake.
	_ = pc.hubReadEnv(t) // hello
	pc.hubSendEnv(t, wire.TypeAuthRequired, c.cfg.AccountID, wire.AuthRequired{})
	_ = pc.hubReadEnv(t) // auth
	pc.hubSendEnv(t, wire.TypeAuthFail, c.cfg.AccountID, wire.AuthFail{
		Code: wire.AuthFailInvalidToken, Reason: "bad token",
	})

	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("Run returned nil; expected fatal auth error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return within 2s after auth_fail")
	}
}

// TestIsFatalAuthErr_DrivenByCodeNotReason guards the auth-fail
// classification against substring leakage: it must key off the typed
// AuthFailCode (via the errFatalAuth sentinel), never the free-form text of
// the error. Every auth_fail code is fatal regardless of its Reason, while a
// recoverable transport error whose message happens to contain a fatal
// keyword must stay recoverable.
func TestIsFatalAuthErr_DrivenByCodeNotReason(t *testing.T) {
	mk := func(code wire.AuthFailCode, reason string) error {
		err := fmt.Errorf("auth_fail: %s (%s)", code, reason)
		if isFatalAuthCode(code) {
			return fmt.Errorf("%w: %w", errFatalAuth, err)
		}
		return err
	}
	cases := []struct {
		name      string
		err       error
		wantFatal bool
	}{
		{"nil", nil, false},
		{"invalid_token", mk(wire.AuthFailInvalidToken, "bad token"), true},
		{"revoked", mk(wire.AuthFailRevoked, "key rotated"), true},
		{"schema_mismatch", mk(wire.AuthFailSchemaMismatch, "redeploy"), true},
		{"account_mismatch", mk(wire.AuthFailAccountMismatch, "check config"), true},
		// Fatal codes trip regardless of a benign/empty Reason.
		{"account_mismatch_empty_reason", mk(wire.AuthFailAccountMismatch, ""), true},
		// The fragility this fix removes: a recoverable transport error whose
		// message contains a fatal keyword must NOT be classified fatal —
		// classification keys off the sentinel, not substring matching.
		{"transport_err_with_fatal_word", fmt.Errorf("read: stream revoked by peer"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isFatalAuthErr(tc.err); got != tc.wantFatal {
				t.Errorf("isFatalAuthErr(%v) = %v, want %v", tc.err, got, tc.wantFatal)
			}
		})
	}
}

func TestPing_RepliesPong(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(nil)
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	pingMsgID := pc.hubSendEnv(t, wire.TypePing, c.cfg.AccountID, wire.Ping{
		ServerNowMs: time.Now().UnixMilli(),
	})
	pong := pc.hubReadEnv(t)
	if pong.Type != wire.TypePong {
		t.Fatalf("expected pong, got %q", pong.Type)
	}
	p, _ := wire.DecodePayload[wire.Pong](pong)
	if p.EchoMsgID != pingMsgID {
		t.Errorf("pong.echo_msg_id = %q, want %q", p.EchoMsgID, pingMsgID)
	}
	if !p.ExchangeReachable {
		t.Errorf("pong.exchange_reachable = false; mock returns true")
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

func TestTradeCommand_MarketHappyPath(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	ex.SetSlippageBps(2.0) // buy fills 1 bps above mid
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID00000000000000000B",
		InstanceID:      "01HKINSTANCE0000000000000A",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    time.Now().UnixMilli() + 60000,
		NowMsAtSaaS:     time.Now().UnixMilli(),
	})

	// Expect ack then order_update.
	ackEnv := pc.hubReadEnv(t)
	if ackEnv.Type != wire.TypeAck {
		t.Fatalf("expected ack, got %q", ackEnv.Type)
	}
	ack, _ := wire.DecodePayload[wire.Ack](ackEnv)
	if ack.Status != wire.AckStatusAccepted {
		t.Errorf("ack.status = %q, want accepted; reason=%q", ack.Status, ack.RejectReason)
	}
	if ack.ExchangeOrderID == "" {
		t.Errorf("ack missing exchange_order_id")
	}

	ouEnv := pc.hubReadEnv(t)
	if ouEnv.Type != wire.TypeOrderUpdate {
		t.Fatalf("expected order_update, got %q", ouEnv.Type)
	}
	ou, _ := wire.DecodePayload[wire.OrderUpdate](ouEnv)
	if ou.Status != wire.OrderStatusFilled {
		t.Errorf("order_update.status = %q", ou.Status)
	}
	if len(ou.Fills) != 1 {
		t.Fatalf("fills len = %d", len(ou.Fills))
	}
	// market buy with +2bps slippage → expect actual_slippage_bps ≈ 2.0
	if got := ou.Fills[0].ActualSlippageBps; got < 1.99 || got > 2.01 {
		t.Errorf("actual_slippage_bps = %v, want ~2.0", got)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

func TestTradeCommand_LimitOrder(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:        wire.IntentKindMicro,
		ClientOrderID:     "01HKCOID0000000000000LIMIT",
		Symbol:            "BTCUSDT",
		Side:              "sell",
		OrderType:         "limit",
		QuantityDecimal:   "0.02",
		LimitPriceDecimal: "50100.00",
		ValidUntilMs:      time.Now().UnixMilli() + 60000,
		NowMsAtSaaS:       time.Now().UnixMilli(),
	})

	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusAccepted {
		t.Errorf("ack.status = %q reason=%q", ack.Status, ack.RejectReason)
	}
	ou, _ := wire.DecodePayload[wire.OrderUpdate](pc.hubReadEnv(t))
	if ou.Status != wire.OrderStatusFilled {
		t.Errorf("status = %q", ou.Status)
	}
	// limit sell at 50100 vs ref 50100 → slippage 0
	if got := ou.Fills[0].ActualSlippageBps; got != 0 {
		t.Errorf("actual_slippage_bps = %v, want 0 (limit at ref)", got)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

func TestTradeCommand_DuplicateClientOrderID(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	tc := wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID0000000000000000DUP",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    time.Now().UnixMilli() + 60000,
		NowMsAtSaaS:     time.Now().UnixMilli(),
	}
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, tc)
	// drain ack + order_update
	_ = pc.hubReadEnv(t)
	_ = pc.hubReadEnv(t)

	// Now send the same TradeCommand again.
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, tc)
	dupAck, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if dupAck.Status != wire.AckStatusDuplicateTerminal {
		t.Errorf("dup ack.status = %q, want duplicate_terminal", dupAck.Status)
	}
	if dupAck.ExchangeOrderID == "" {
		t.Errorf("dup ack missing exchange_order_id from prior submit")
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

func TestTradeCommand_ExpiredRejected(t *testing.T) {
	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClient(t, []*pipeConn{pc}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	now := time.Now().UnixMilli()
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID0000000000000EXPRD",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    now - 1000, // expired 1s ago
		NowMsAtSaaS:     now,
	})

	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusExpired {
		t.Errorf("ack.status = %q, want expired", ack.Status)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

func TestBackoff_DialRetries(t *testing.T) {
	// Dialer always fails; verify Run loops several times before
	// ctx is cancelled.
	dialer := &staticDialer{dialErr: errors.New("dial failed")}
	cfg := Config{
		AgentID:   "01HKAGENT00000000000000000",
		AccountID: "01HKACCT00000000000000000A",
		SaaSURL:   "ws://test",
		SaaSToken: "agt_01HKAGENT00000000000000000_FAKESECRET",
		Exchange:  ExchangeConfig{Name: "mock"},
	}
	c := NewClient(cfg, NewMockExchange(nil), NewMemoryStore(), Options{
		Dialer:        dialer,
		Backoff:       []time.Duration{1 * time.Millisecond, 2 * time.Millisecond, 4 * time.Millisecond},
		BackoffJitter: func() float64 { return 1.0 },
	})
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	_ = c.Run(ctx)

	if got := dialer.dialN.Load(); got < 3 {
		t.Errorf("dial attempts = %d, want >=3 over 50ms", got)
	}
}

func TestBackoff_ResetsAfterSuccessfulSession(t *testing.T) {
	pc1 := newPipeConn()
	pc2 := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClient(t, []*pipeConn{pc1, pc2}, ex)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	// First session succeeds → backoff resets.
	runHubHandshake(t, pc1, c.cfg.AccountID)
	// Advance backoff index by failing... but it should reset after Ready.
	// Verify by advancing manually first then checking it resets.
	// (We can't easily inspect backoffIdx; instead, force a disconnect
	// and verify the second dial happens after the *first* backoff entry.)
	_ = pc1.Close()
	// Second session also succeeds.
	runHubHandshake(t, pc2, c.cfg.AccountID)
	if got := c.backoffIdx.Load(); got != 0 {
		t.Errorf("backoffIdx after second Ready = %d, want 0", got)
	}

	cancel()
	_ = pc2.Close()
	<-errCh
}

func TestKillSwitch_AckAccepted(t *testing.T) {
	pc := newPipeConn()
	c := newTestClient(t, []*pipeConn{pc}, NewMockExchange(nil))
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)
	pc.hubSendEnv(t, wire.TypeKillSwitch, c.cfg.AccountID, wire.KillSwitch{
		Reason:         wire.KillSwitchManualAdminAction,
		OperatorUserID: "01HKOPER00000000000000000A",
		Scope:          wire.KillSwitchScopeAll,
	})
	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusAccepted {
		t.Errorf("kill_switch ack.status = %q", ack.Status)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

// ===== Phase 7.11: OrderEventStreamer wiring + onOrderEvent =====

// streamerMockExchange is MockExchange + Subscribe(), used by tests that
// need to exercise the OrderEventStreamer code path.
type streamerMockExchange struct {
	*MockExchange
	cb func(OrderEvent)
}

func newStreamerMockExchange() *streamerMockExchange {
	return &streamerMockExchange{
		MockExchange: NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)}),
	}
}

func (s *streamerMockExchange) Subscribe(cb func(OrderEvent)) { s.cb = cb }

func (s *streamerMockExchange) emit(ev OrderEvent) {
	if s.cb != nil {
		s.cb(ev)
	}
}

// compile-time confirmation that streamerMockExchange satisfies both interfaces.
var _ Exchange = (*streamerMockExchange)(nil)
var _ OrderEventStreamer = (*streamerMockExchange)(nil)

func TestNewClient_SubscribesWhenExchangeIsStreamer(t *testing.T) {
	ex := newStreamerMockExchange()
	c := newTestClient(t, []*pipeConn{}, ex)
	if ex.cb == nil {
		t.Fatal("NewClient should have called Subscribe on a streamer-capable exchange")
	}
	// Sanity: cb is wired to c.onOrderEvent (compile + identity check).
	_ = c // c retains the registered callback path; no need to invoke here.
}

func TestNewClient_DoesNotSubscribeWhenExchangeIsNotStreamer(t *testing.T) {
	// Plain *MockExchange does not implement OrderEventStreamer.
	var ex Exchange = NewMockExchange(nil)
	if _, ok := ex.(OrderEventStreamer); ok {
		t.Fatal("plain MockExchange must NOT satisfy OrderEventStreamer (test premise broken)")
	}
	c := newTestClient(t, []*pipeConn{}, ex)
	if c == nil {
		t.Fatal("NewClient returned nil")
	}
}

// onOrderEventTestFixture wires a Client + pipeConn + streamer mock
// directly, bypassing Run(). Used for unit-style assertions on
// onOrderEvent. The pipeConn is installed manually so c.connMu/c.conn
// already exists, mimicking the post-handshake state.
func newOnOrderEventFixture(t *testing.T) (*Client, *pipeConn, *streamerMockExchange) {
	t.Helper()
	pc := newPipeConn()
	ex := newStreamerMockExchange()
	c := newTestClient(t, []*pipeConn{}, ex)
	c.connMu.Lock()
	c.conn = pc
	c.connMu.Unlock()
	return c, pc, ex
}

func TestOnOrderEvent_BuildsWireOrderUpdateForFill(t *testing.T) {
	c, pc, ex := newOnOrderEventFixture(t)

	// Pre-populate idempotency with a limit-order accepted record.
	ref := decimal.RequireFromString("45000")
	rec := IdempotencyRecord{
		ClientOrderID:   "COID-EV-1",
		ExchangeOrderID: "X-1",
		Status:          IdempotencyStatusAccepted,
		MarketRef:       ref,
		SubmittedAtMs:   1_700_000_000_000,
		LastUpdatedMs:   1_700_000_000_000,
	}
	if err := c.idempotency.Put(rec); err != nil {
		t.Fatalf("idempotency.Put: %v", err)
	}

	ex.emit(OrderEvent{
		ClientOrderID:   "COID-EV-1",
		ExchangeOrderID: "X-1",
		Status:          wire.OrderStatusFilled,
		Side:            "buy",
		Fill: &ExchangeFill{
			FillQuantity:       decimal.RequireFromString("0.001"),
			FillPrice:          decimal.RequireFromString("45045"), // +10 bps above ref
			FillFeeAsset:       "USDT",
			FillFeeAmount:      decimal.RequireFromString("0.045"),
			FilledAtExchangeMs: 1_700_000_001_000,
		},
		CumulativeFillQuantity: decimal.RequireFromString("0.001"),
	})

	env := pc.hubReadEnv(t)
	if env.Type != wire.TypeOrderUpdate {
		t.Fatalf("type = %q, want order_update", env.Type)
	}
	ou, _ := wire.DecodePayload[wire.OrderUpdate](env)
	if ou.ClientOrderID != "COID-EV-1" {
		t.Errorf("ClientOrderID = %q", ou.ClientOrderID)
	}
	if ou.ExchangeOrderID != "X-1" {
		t.Errorf("ExchangeOrderID = %q", ou.ExchangeOrderID)
	}
	if ou.Status != wire.OrderStatusFilled {
		t.Errorf("Status = %q, want filled", ou.Status)
	}
	if len(ou.Fills) != 1 {
		t.Fatalf("len(Fills) = %d, want 1", len(ou.Fills))
	}
	// (45045-45000)/45000 * 10000 = 10 bps
	if ou.Fills[0].ActualSlippageBps < 9.9 || ou.Fills[0].ActualSlippageBps > 10.1 {
		t.Errorf("ActualSlippageBps = %f, want ~10", ou.Fills[0].ActualSlippageBps)
	}
	if ou.CumulativeFilledQuantityDecimal == "" {
		t.Error("CumulativeFilledQuantityDecimal empty")
	}

	// Idempotency status flipped to filled.
	got, ok, _ := c.idempotency.Get("COID-EV-1")
	if !ok || got.Status != IdempotencyStatusFilled {
		t.Errorf("idem status = %v, want filled", got.Status)
	}
}

func TestOnOrderEvent_DropsWhenUnknownClientOrderID(t *testing.T) {
	c, pc, ex := newOnOrderEventFixture(t)

	ex.emit(OrderEvent{
		ClientOrderID: "COID-UNKNOWN",
		Status:        wire.OrderStatusFilled,
		Side:          "buy",
		Fill:          &ExchangeFill{FillQuantity: decimal.NewFromInt(1), FillPrice: decimal.NewFromInt(1)},
	})

	// No frame should arrive — drain channel with a tight timeout.
	select {
	case frame := <-pc.serverWrites:
		t.Errorf("unexpected frame for unknown client_order_id: %s", string(frame))
	case <-time.After(50 * time.Millisecond):
	}
	_ = c
}

func TestOnOrderEvent_DropsWhenNoActiveConn(t *testing.T) {
	c, pc, ex := newOnOrderEventFixture(t)
	// Take the conn back away to mimic between-session state.
	c.connMu.Lock()
	c.conn = nil
	c.connMu.Unlock()

	_ = c.idempotency.Put(IdempotencyRecord{
		ClientOrderID: "COID-OFFLINE",
		Status:        IdempotencyStatusAccepted,
		MarketRef:     decimal.NewFromInt(100),
	})

	ex.emit(OrderEvent{
		ClientOrderID: "COID-OFFLINE",
		Status:        wire.OrderStatusFilled,
		Side:          "buy",
		Fill: &ExchangeFill{
			FillQuantity: decimal.NewFromInt(1),
			FillPrice:    decimal.NewFromInt(100),
		},
	})
	select {
	case frame := <-pc.serverWrites:
		t.Errorf("unexpected frame while conn=nil: %s", string(frame))
	case <-time.After(50 * time.Millisecond):
	}
}

func TestOnOrderEvent_PartialFilledDoesNotChangeIdemStatus(t *testing.T) {
	c, pc, ex := newOnOrderEventFixture(t)
	_ = c.idempotency.Put(IdempotencyRecord{
		ClientOrderID: "COID-PART",
		Status:        IdempotencyStatusAccepted,
		MarketRef:     decimal.NewFromInt(50000),
	})
	ex.emit(OrderEvent{
		ClientOrderID:          "COID-PART",
		Status:                 wire.OrderStatusPartialFilled,
		Side:                   "buy",
		Fill:                   &ExchangeFill{FillQuantity: decimal.RequireFromString("0.0005"), FillPrice: decimal.NewFromInt(50000)},
		CumulativeFillQuantity: decimal.RequireFromString("0.0005"),
	})

	env := pc.hubReadEnv(t)
	ou, _ := wire.DecodePayload[wire.OrderUpdate](env)
	if ou.Status != wire.OrderStatusPartialFilled {
		t.Errorf("wire status = %q, want partial_filled", ou.Status)
	}
	got, _, _ := c.idempotency.Get("COID-PART")
	if got.Status != IdempotencyStatusAccepted {
		t.Errorf("idem status = %v after partial fill, want accepted (unchanged)", got.Status)
	}
}

func TestOnOrderEvent_CancelledHasNoFills(t *testing.T) {
	c, pc, ex := newOnOrderEventFixture(t)
	_ = c.idempotency.Put(IdempotencyRecord{
		ClientOrderID: "COID-CANCEL",
		Status:        IdempotencyStatusAccepted,
		MarketRef:     decimal.NewFromInt(50000),
	})
	ex.emit(OrderEvent{ClientOrderID: "COID-CANCEL", Status: wire.OrderStatusCancelled})

	env := pc.hubReadEnv(t)
	ou, _ := wire.DecodePayload[wire.OrderUpdate](env)
	if ou.Status != wire.OrderStatusCancelled {
		t.Errorf("Status = %q, want cancelled", ou.Status)
	}
	if len(ou.Fills) != 0 {
		t.Errorf("len(Fills) = %d, want 0 on cancellation", len(ou.Fills))
	}
	got, _, _ := c.idempotency.Get("COID-CANCEL")
	if got.Status != IdempotencyStatusCancelled {
		t.Errorf("idem status = %v after cancellation, want cancelled", got.Status)
	}
}

func TestOnOrderEvent_FallsBackToFillQtyWhenCumulativeZero(t *testing.T) {
	c, pc, ex := newOnOrderEventFixture(t)
	_ = c.idempotency.Put(IdempotencyRecord{
		ClientOrderID: "COID-NOCUM",
		Status:        IdempotencyStatusAccepted,
		MarketRef:     decimal.NewFromInt(50000),
	})
	ex.emit(OrderEvent{
		ClientOrderID: "COID-NOCUM",
		Status:        wire.OrderStatusFilled,
		Side:          "buy",
		Fill: &ExchangeFill{
			FillQuantity: decimal.RequireFromString("0.002"),
			FillPrice:    decimal.NewFromInt(50000),
		},
		// CumulativeFillQuantity intentionally zero.
	})

	env := pc.hubReadEnv(t)
	ou, _ := wire.DecodePayload[wire.OrderUpdate](env)
	// Should be 0.002 (from fill qty) not "" or "0".
	if !strings.Contains(ou.CumulativeFilledQuantityDecimal, "0.00200000") {
		t.Errorf("CumulativeFilledQuantityDecimal = %q, want fallback 0.002", ou.CumulativeFilledQuantityDecimal)
	}
}

// ---- idempotency fail-open tests (S5B-1, S5B-2, S5B-3) ----

// alwaysErrStore is an IdempotencyStore whose Get always returns an error.
// Used to drive the S5B-2 / S5B-3 fail-closed paths.
type alwaysErrStore struct{ err error }

func (s *alwaysErrStore) Get(_ string) (IdempotencyRecord, bool, error) {
	return IdempotencyRecord{}, false, s.err
}
func (s *alwaysErrStore) Put(_ IdempotencyRecord) error { return nil }
func (s *alwaysErrStore) UpdateStatus(_ string, _ IdempotencyStatus, _ string, _ int64) error {
	return nil
}

// newTestClientWithIdem is newTestClient with an explicit IdempotencyStore.
func newTestClientWithIdem(t *testing.T, conns []*pipeConn, ex Exchange, idem IdempotencyStore) *Client {
	t.Helper()
	cfg := Config{
		AgentID:   "01HKAGENT00000000000000000",
		AccountID: "01HKACCT00000000000000000A",
		SaaSURL:   "ws://test/api/v1/ws/agent",
		SaaSToken: "agt_01HKAGENT00000000000000000_FAKESECRET",
		Exchange:  ExchangeConfig{Name: "mock"},
	}
	dialer := &staticDialer{conns: conns}
	return NewClient(cfg, ex, idem, Options{
		Dialer:        dialer,
		Backoff:       []time.Duration{5 * time.Millisecond, 10 * time.Millisecond},
		BackoffJitter: func() float64 { return 1.0 },
		NowFn:         func() time.Time { return time.Unix(1700000000, 0) },
	})
}

// newOnOrderEventFixtureWithIdem is newOnOrderEventFixture with a custom store.
func newOnOrderEventFixtureWithIdem(t *testing.T, idem IdempotencyStore) (*Client, *pipeConn, *streamerMockExchange) {
	t.Helper()
	pc := newPipeConn()
	ex := newStreamerMockExchange()
	c := newTestClientWithIdem(t, []*pipeConn{}, ex, idem)
	c.connMu.Lock()
	c.conn = pc
	c.connMu.Unlock()
	return c, pc, ex
}

// TestHandleTradeCommand_DuplicateTerminalBeforeExpiry (S5B-1): a replay of
// an already-filled order with a past valid_until_ms must return
// duplicate_terminal, not expired.
func TestHandleTradeCommand_DuplicateTerminalBeforeExpiry(t *testing.T) {
	idem := NewMemoryStore()
	_ = idem.Put(IdempotencyRecord{
		ClientOrderID:   "01HKCOID000000000000FILLED1",
		ExchangeOrderID: "ex-order-filled-1",
		Status:          IdempotencyStatusFilled,
	})

	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClientWithIdem(t, []*pipeConn{pc}, ex, idem)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	// Replay with an already-expired timestamp.
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID000000000000FILLED1",
		InstanceID:      "01HKINSTANCE0000000000000A",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    1, // far in the past
		NowMsAtSaaS:     1,
	})

	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusDuplicateTerminal {
		t.Errorf("ack.status = %q, want duplicate_terminal (not expired)", ack.Status)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

// TestHandleTradeCommand_DuplicateTerminalWhileFrozen (S5B-1): a replay of
// an already-filled order while the agent is frozen must return
// duplicate_terminal, not rejected.
func TestHandleTradeCommand_DuplicateTerminalWhileFrozen(t *testing.T) {
	idem := NewMemoryStore()
	_ = idem.Put(IdempotencyRecord{
		ClientOrderID:   "01HKCOID000000000000FILLED2",
		ExchangeOrderID: "ex-order-filled-2",
		Status:          IdempotencyStatusFilled,
	})

	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClientWithIdem(t, []*pipeConn{pc}, ex, idem)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	// Freeze the agent via kill_switch.
	pc.hubSendEnv(t, wire.TypeKillSwitch, c.cfg.AccountID, wire.KillSwitch{
		Reason: wire.KillSwitchDiscrepancyDetected, Scope: wire.KillSwitchScopeAll,
	})
	if ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t)); ack.Status != wire.AckStatusAccepted {
		t.Fatalf("kill_switch ack.status = %q, want accepted", ack.Status)
	}

	// Replay the already-filled order while frozen.
	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID000000000000FILLED2",
		InstanceID:      "01HKINSTANCE0000000000000A",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    time.Now().UnixMilli() + 60000,
		NowMsAtSaaS:     time.Now().UnixMilli(),
	})

	ack, _ := wire.DecodePayload[wire.Ack](pc.hubReadEnv(t))
	if ack.Status != wire.AckStatusDuplicateTerminal {
		t.Errorf("ack.status = %q, want duplicate_terminal (not rejected)", ack.Status)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

// TestHandleTradeCommand_IdempotencyGetError_NoSubmit (S5B-2): when
// idempotency.Get returns an error, the handler must return an internal
// error response and must not submit to the exchange.
func TestHandleTradeCommand_IdempotencyGetError_NoSubmit(t *testing.T) {
	errStore := &alwaysErrStore{err: errors.New("sqlite busy")}

	pc := newPipeConn()
	ex := NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	c := newTestClientWithIdem(t, []*pipeConn{pc}, ex, errStore)
	cancel, errCh := runClientInBg(t, c)
	defer cancel()

	runHubHandshake(t, pc, c.cfg.AccountID)

	pc.hubSendEnv(t, wire.TypeTradeCommand, c.cfg.AccountID, wire.TradeCommand{
		IntentKind:      wire.IntentKindMacro,
		ClientOrderID:   "01HKCOID000000000000ERRGET",
		InstanceID:      "01HKINSTANCE0000000000000A",
		Symbol:          "BTCUSDT",
		Side:            "buy",
		OrderType:       "market",
		QuantityDecimal: "0.01",
		ValidUntilMs:    time.Now().UnixMilli() + 60000,
		NowMsAtSaaS:     time.Now().UnixMilli(),
	})

	// Expect an error_msg frame, not an Ack.
	env := pc.hubReadEnv(t)
	if env.Type != wire.TypeError {
		t.Errorf("response type = %q, want error_msg (no submit on Get error)", env.Type)
	}
	em, _ := wire.DecodePayload[wire.Error](env)
	if em.Code != wire.ErrorCodeInternalError {
		t.Errorf("error code = %q, want internal_error", em.Code)
	}
	if !strings.Contains(em.Message, "idempotency read") {
		t.Errorf("error message = %q, want it to mention idempotency", em.Message)
	}

	cancel()
	_ = pc.Close()
	<-errCh
}

// TestOnOrderEvent_IdempotencyGetError_FillNotDropped (S5B-3): when
// onOrderEvent's idempotency.Get returns an error, the fill must still
// appear in the delta_report buffer and an OrderUpdate must be sent.
func TestOnOrderEvent_IdempotencyGetError_FillNotDropped(t *testing.T) {
	errStore := &alwaysErrStore{err: errors.New("sqlite locked")}
	c, pc, ex := newOnOrderEventFixtureWithIdem(t, errStore)

	ex.emit(OrderEvent{
		ClientOrderID:   "COID-ERRFILL",
		ExchangeOrderID: "ex-order-errfill",
		Status:          wire.OrderStatusFilled,
		Side:            "buy",
		Fill: &ExchangeFill{
			FillQuantity: decimal.RequireFromString("0.01"),
			FillPrice:    decimal.NewFromInt(65000),
		},
		CumulativeFillQuantity: decimal.RequireFromString("0.01"),
	})

	// Fill must appear in delta buffer.
	fills, _ := c.delta.drain()
	if len(fills) != 1 {
		t.Fatalf("delta fills = %d, want 1", len(fills))
	}
	if fills[0].ClientOrderID != "COID-ERRFILL" {
		t.Errorf("delta fill client_order_id = %q, want COID-ERRFILL", fills[0].ClientOrderID)
	}

	// OrderUpdate must be sent on the conn.
	env := pc.hubReadEnv(t)
	if env.Type != wire.TypeOrderUpdate {
		t.Errorf("frame type = %q, want order_update", env.Type)
	}
	ou, _ := wire.DecodePayload[wire.OrderUpdate](env)
	if ou.ClientOrderID != "COID-ERRFILL" {
		t.Errorf("order_update.client_order_id = %q, want COID-ERRFILL", ou.ClientOrderID)
	}
	if len(ou.Fills) != 1 {
		t.Errorf("order_update.fills = %d, want 1", len(ou.Fills))
	}
}
