package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

// e2eTokenStore is a self-contained in-memory agentauth.TokenStore for
// the no-DB end-to-end test.
type e2eTokenStore struct {
	mu   sync.Mutex
	rows map[string]*store.AgentToken
}

func (f *e2eTokenStore) GetByAgentID(_ context.Context, id string) (*store.AgentToken, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		cp := *r
		return &cp, nil
	}
	return nil, nil
}
func (f *e2eTokenStore) Create(_ context.Context, row *store.AgentToken) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	cp := *row
	f.rows[row.AgentID] = &cp
	return nil
}
func (f *e2eTokenStore) Revoke(_ context.Context, id string, now time.Time) error { return nil }
func (f *e2eTokenStore) TouchLastSeen(_ context.Context, id string, now time.Time) error {
	return nil
}

// TestE2E_AutoFreezeHaltsAgent is the full auto-trigger chain over a real
// WebSocket, no DB: maybeAutoFreeze (debounce N=2) → Hub.SendKillSwitch →
// real agent latches frozen → a dispatched order is rejected. The
// reconcile→drifts segment is covered by reconcilePositions unit tests, so
// this starts from the drift result and proves the live half end to end.
func TestE2E_AutoFreezeHaltsAgent(t *testing.T) {
	ctx := context.Background()

	tokens := &e2eTokenStore{rows: map[string]*store.AgentToken{}}
	authSvc := agentauth.NewService(tokens).WithBcryptCost(bcrypt.MinCost)
	const accountID = "01HKE2EAUTOACCT000000000A"
	created, err := authSvc.CreateToken(ctx, accountID, "e2e-auto-fixture")
	if err != nil {
		t.Fatalf("CreateToken: %v", err)
	}

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
		PingInterval: time.Hour, PongTimeout: time.Hour, PingMisses: 3,
		AuthTimeout: 5 * time.Second, StateSyncTimeout: 5 * time.Second,
		WriteTimeout: 5 * time.Second, OnAgentMessage: captureFn,
	})

	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/ws/agent", hub.ServeWS)
	srv := httptest.NewServer(mux)
	defer srv.Close()
	wsURL := strings.Replace(srv.URL, "http://", "ws://", 1) + "/api/v1/ws/agent"

	exchange := agent.NewMockExchange(map[string]decimal.Decimal{"BTCUSDT": decimal.NewFromInt(50000)})
	exchange.SetPosition(agent.Position{Symbol: "USDT", Free: decimal.NewFromInt(100000), Locked: decimal.Zero})

	cli := agent.NewClient(
		agent.Config{
			AgentID: created.AgentID, AccountID: accountID, SaaSURL: wsURL,
			SaaSToken: created.Plaintext, Exchange: agent.ExchangeConfig{Name: "mock"},
		},
		exchange, agent.NewMemoryStore(),
		agent.Options{Backoff: []time.Duration{5 * time.Millisecond}},
	)
	agentCtx, cancelAgent := context.WithCancel(context.Background())
	agentDone := make(chan error, 1)
	go func() { agentDone <- cli.Run(agentCtx) }()
	defer func() {
		cancelAgent()
		select {
		case <-agentDone:
		case <-time.After(2 * time.Second):
		}
	}()

	if !waitReady(hub, accountID, 3*time.Second) {
		t.Fatalf("agent never reached Ready")
	}

	// Drive the SaaS-side auto-freeze decision against the real hub.
	fa := &fakeAuditor{}
	h := newAgentMessageHandler(nil, nil, nil, nil, nil) // logger defaults to slog.Default()
	h.SetKillSwitchSender(hub)
	h.auditor = fa
	breach := []driftResult{{Asset: "BTC", DriftBps: 300, Flagged: true}} // > 200bps freeze line
	managed := managedSet("BTC")

	h.maybeAutoFreeze(ctx, accountID, breach, managed) // streak 1 — no fire
	h.maybeAutoFreeze(ctx, accountID, breach, managed) // streak 2 — fire → SendKillSwitch

	// The kill is acked once the agent processes it; wait for that ack so
	// the frozen latch is set before we dispatch.
	if !waitCaps(&capMu, &caps, 1, 3*time.Second) {
		t.Fatalf("no kill ack within 3s — auto-freeze did not reach the agent")
	}
	if len(fa.rows) != 1 {
		t.Errorf("audit rows = %d, want 1 (auto kill)", len(fa.rows))
	}

	// A dispatched order must now be rejected (frozen), not filled.
	const postKillCOID = "01HKE2EAUTOCOID0000000001"
	orders := []strategy.OrderIntent{{
		Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy,
		OrderType: strategy.OrderTypeMarket, QuantityUSD: 1000,
		ClientOrderID: postKillCOID, ValidUntilMs: time.Now().UnixMilli() + 60_000,
	}}
	if err := hub.Dispatch(ctx, "01HKINSTANCE0000000000000A", accountID, "BTCUSDT", 50000.0, orders); err != nil {
		t.Fatalf("hub.Dispatch: %v", err)
	}
	if !waitCaps(&capMu, &caps, 2, 3*time.Second) {
		t.Fatalf("no trade ack within 3s (got %d caps)", len(caps))
	}

	capMu.Lock()
	defer capMu.Unlock()
	var tradeAck *wire.Ack
	for _, c := range caps {
		if c.t != wire.TypeAck {
			continue
		}
		a, _ := wire.DecodePayload[wire.Ack](c.env)
		if a.ClientOrderID == postKillCOID {
			tradeAck = a
		}
	}
	if tradeAck == nil {
		t.Fatal("no ack for the post-kill trade_command")
	}
	if tradeAck.Status != wire.AckStatusRejected {
		t.Errorf("post-auto-kill trade ack.status = %q, want rejected", tradeAck.Status)
	}
	if !strings.Contains(tradeAck.RejectReason, "frozen") {
		t.Errorf("reject reason = %q, want it to mention 'frozen'", tradeAck.RejectReason)
	}
}

func waitReady(hub *wshub.Hub, accountID string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cn, err := hub.Registry().Get(accountID); err == nil && cn.IsReady() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func waitCaps[T any](mu *sync.Mutex, caps *[]T, n int, timeout time.Duration) bool {
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
