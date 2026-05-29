package wshub_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"
	"golang.org/x/crypto/bcrypt"

	"quantlab/internal/agent"
	"quantlab/internal/saas/agentauth"
	"quantlab/internal/saas/store"
	"quantlab/internal/saas/wshub"
	"quantlab/internal/strategy"
	"quantlab/internal/wire"
)

// fakeTokenStore is a self-contained in-memory TokenStore (same shape
// as the one inside wshub's package-private tests; redeclared here
// because this is an external _test package).
type fakeTokenStore struct {
	mu   sync.Mutex
	rows map[string]*store.AgentToken
}

func newFakeTokenStore() *fakeTokenStore {
	return &fakeTokenStore{rows: make(map[string]*store.AgentToken)}
}
func (f *fakeTokenStore) GetByAgentID(_ context.Context, id string) (*store.AgentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}
func (f *fakeTokenStore) Create(_ context.Context, row *store.AgentToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *row
	f.rows[row.AgentID] = &cp
	return nil
}
func (f *fakeTokenStore) Revoke(_ context.Context, id string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok && r.RevokedAt == nil {
		r.RevokedAt = &now
	}
	return nil
}
func (f *fakeTokenStore) TouchLastSeen(_ context.Context, id string, now time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		r.LastSeenAt = &now
	}
	return nil
}

// TestE2E_TradeCommandRoundTrip is the integration smoke test that
// proves wshub.Hub and agent.Client speak the wire protocol over a
// real TCP/WebSocket loopback.
//
// Flow:
//  1. Spin up agentauth.Service backed by fakeTokenStore; mint a token.
//  2. Spin up wshub.Hub with OnAgentMessage capturing Ack/OrderUpdate.
//  3. Spin up httptest.Server exposing /api/v1/ws/agent → hub.ServeWS.
//  4. Spin up agent.Client dialing ws://<httptest>/api/v1/ws/agent
//     with a MockExchange (+2bps buy slippage).
//  5. Wait for the agent to reach Ready (hub.Registry has the conn).
//  6. Call hub.Dispatch with one market buy → expect Ack(accepted)
//     and OrderUpdate(filled) to flow back through the captured hook.
//  7. Assert ActualSlippageBps ≈ 2.0 (the exchange's configured slippage).
func TestE2E_TradeCommandRoundTrip(t *testing.T) {
	ctx := context.Background()

	// 1. Auth
	authSvc := agentauth.NewService(newFakeTokenStore()).WithBcryptCost(bcrypt.MinCost)
	const accountID = "01HKE2EACCT00000000000000A"
	created, err := authSvc.CreateToken(ctx, accountID, "e2e-fixture")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

	// 2. Hub with capture hook
	type captured struct {
		t   wire.MessageType
		env wire.Envelope
	}
	var capMu sync.Mutex
	var caps []captured
	captureFn := func(_ context.Context, _ string, env wire.Envelope) error {
		capMu.Lock()
		defer capMu.Unlock()
		caps = append(caps, captured{env.Type, env})
		return nil
	}

	hub := wshub.New(authSvc, wshub.Config{
		PingInterval:     time.Hour, // disable auto-ping during the short test
		PongTimeout:      time.Hour,
		PingMisses:       3,
		AuthTimeout:      5 * time.Second,
		StateSyncTimeout: 5 * time.Second,
		WriteTimeout:     5 * time.Second,
		OnAgentMessage:   captureFn,
	})

	// 3. httptest server. Serve ServeWS on path /api/v1/ws/agent and
	//    404 elsewhere.
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ws/agent", hub.ServeWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/v1/ws/agent"

	// 4. Agent
	exchange := agent.NewMockExchange(map[string]decimal.Decimal{
		"BTCUSDT": decimal.NewFromInt(50000),
	})
	exchange.SetSlippageBps(2.0) // market buy fills +2bps above mid
	exchange.SetPosition(agent.Position{
		Symbol: "USDT", Free: decimal.NewFromInt(100000), Locked: decimal.Zero,
	})

	cli := agent.NewClient(
		agent.Config{
			AgentID:   created.AgentID,
			AccountID: accountID,
			SaaSURL:   wsURL,
			SaaSToken: created.Plaintext,
			Exchange:  agent.ExchangeConfig{Name: "mock"},
		},
		exchange,
		agent.NewMemoryStore(),
		agent.Options{
			Backoff: []time.Duration{5 * time.Millisecond}, // fast retry on fail
		},
	)

	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- cli.Run(agentCtx) }()
	defer func() {
		cancelAgent()
		select {
		case <-agentDone:
		case <-time.After(2 * time.Second):
			t.Logf("agent did not exit within 2s after cancel")
		}
	}()

	// 5. Wait for Ready
	if !waitForReady(t, hub, accountID, 3*time.Second) {
		t.Fatalf("agent never reached Ready")
	}

	// 6. Dispatch one market buy
	orders := []strategy.OrderIntent{{
		Kind:          strategy.OrderKindMacro,
		Side:          strategy.OrderSideBuy,
		OrderType:     strategy.OrderTypeMarket,
		QuantityUSD:   1000,
		ClientOrderID: "01HKE2ECOID000000000000001",
		ValidUntilMs:  time.Now().UnixMilli() + 60_000,
	}}
	if err := hub.Dispatch(ctx, "01HKINSTANCE0000000000000A", accountID, "BTCUSDT", 50000.0, orders); err != nil {
		t.Fatalf("hub.Dispatch: %v", err)
	}

	// 7. Wait for Ack + OrderUpdate via the capture hook
	if !waitForCaps(t, &capMu, &caps, 2, 3*time.Second) {
		t.Fatalf("did not receive both Ack and OrderUpdate within 3s (got %d caps)",
			len(caps))
	}

	capMu.Lock()
	defer capMu.Unlock()

	var ack *wire.Ack
	var ou *wire.OrderUpdate
	for _, c := range caps {
		switch c.t {
		case wire.TypeAck:
			a, err := wire.DecodePayload[wire.Ack](c.env)
			if err != nil {
				t.Fatalf("decode ack: %v", err)
			}
			ack = a
		case wire.TypeOrderUpdate:
			o, err := wire.DecodePayload[wire.OrderUpdate](c.env)
			if err != nil {
				t.Fatalf("decode order_update: %v", err)
			}
			ou = o
		}
	}
	if ack == nil {
		t.Fatalf("no Ack received")
	}
	if ack.Status != wire.AckStatusAccepted {
		t.Errorf("ack.status = %q (reason=%q), want accepted",
			ack.Status, ack.RejectReason)
	}
	if ack.ClientOrderID != "01HKE2ECOID000000000000001" {
		t.Errorf("ack.client_order_id = %q", ack.ClientOrderID)
	}
	if ack.ExchangeOrderID == "" {
		t.Errorf("ack missing exchange_order_id")
	}

	if ou == nil {
		t.Fatalf("no OrderUpdate received")
	}
	if ou.Status != wire.OrderStatusFilled {
		t.Errorf("order_update.status = %q", ou.Status)
	}
	if len(ou.Fills) != 1 {
		t.Fatalf("fills len = %d, want 1", len(ou.Fills))
	}
	if got := ou.Fills[0].ActualSlippageBps; got < 1.99 || got > 2.01 {
		t.Errorf("actual_slippage_bps = %v, want ~2.0", got)
	}
	if ou.Fills[0].FillFeeAsset != "USDT" {
		t.Errorf("fill_fee_asset = %q", ou.Fills[0].FillFeeAsset)
	}
}

// TestE2E_HandshakeRejectsBadToken proves auth_fail surfaces over real
// WS and the Agent treats it as fatal (no retry storm).
func TestE2E_HandshakeRejectsBadToken(t *testing.T) {
	ctx := context.Background()
	authSvc := agentauth.NewService(newFakeTokenStore()).WithBcryptCost(bcrypt.MinCost)
	// Don't mint a token at all — every Auth attempt fails with
	// ErrUnknownAgent → AuthFailInvalidToken.

	hub := wshub.New(authSvc, wshub.Config{
		PingInterval:     time.Hour,
		PongTimeout:      time.Hour,
		PingMisses:       3,
		AuthTimeout:      5 * time.Second,
		StateSyncTimeout: 5 * time.Second,
		WriteTimeout:     5 * time.Second,
	})
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ws/agent", hub.ServeWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/v1/ws/agent"

	cli := agent.NewClient(
		agent.Config{
			AgentID:   "01HKE2EAGENT00000000000000",
			AccountID: "01HKE2EACCT00000000000000A",
			SaaSURL:   wsURL,
			SaaSToken: "agt_01HKE2EAGENT00000000000000_AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
			Exchange:  agent.ExchangeConfig{Name: "mock"},
		},
		agent.NewMockExchange(nil),
		agent.NewMemoryStore(),
		agent.Options{
			Backoff: []time.Duration{50 * time.Millisecond, 100 * time.Millisecond},
		},
	)

	agentCtx, cancelAgent := context.WithTimeout(ctx, 3*time.Second)
	defer cancelAgent()
	runErr := cli.Run(agentCtx)

	// Agent should exit with an error containing "invalid_token" or
	// "schema_mismatch" — fatal-auth path returns from Run.
	if runErr == nil {
		t.Fatal("Run returned nil; expected fatal auth error")
	}
	if !strings.Contains(runErr.Error(), "invalid_token") &&
		!strings.Contains(runErr.Error(), "auth_fail") {
		t.Errorf("Run err = %v, want auth_fail", runErr)
	}
	_ = ctx
}

// waitForReady polls hub.Registry until a Ready Connection exists for
// accountID, capped at timeout.
func waitForReady(t *testing.T, hub *wshub.Hub, accountID string, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		cn, err := hub.Registry().Get(accountID)
		if err == nil && cn.IsReady() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// waitForCaps polls the capture slice until it has ≥ n entries.
func waitForCaps[T any](t *testing.T, mu *sync.Mutex, caps *[]T, n int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(*caps)
		mu.Unlock()
		if got >= n {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

// _ = atomic to silence unused import in some Go versions if the helpers
// drop the only atomic reference.
var _ = atomic.Int32{}
