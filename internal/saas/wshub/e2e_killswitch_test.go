package wshub_test

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
	"quantlab/internal/saas/wshub"
	"quantlab/internal/strategy"
	"quantlab/internal/wire"
)

// TestE2E_KillSwitchFreezesAgent verifies kill_switch Option 3 step 1+2
// over a real WebSocket: hub.SendKillSwitch latches the agent frozen,
// after which a dispatched order is acked rejected (with a "frozen"
// reason) instead of accepted+filled.
func TestE2E_KillSwitchFreezesAgent(t *testing.T) {
	ctx := context.Background()

	authSvc := agentauth.NewService(newFakeTokenStore()).WithBcryptCost(bcrypt.MinCost)
	const accountID = "01HKE2EKILLACCT0000000000A"
	const postKillCOID = "01HKE2EKILLCOID0000000001"
	created, err := authSvc.CreateToken(ctx, accountID, "e2e-kill-fixture")
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
			t.Logf("agent did not exit within 2s after cancel")
		}
	}()

	if !waitForReady(t, hub, accountID, 3*time.Second) {
		t.Fatalf("agent never reached Ready")
	}

	// Kill via the control plane.
	if err := hub.SendKillSwitch(accountID, wire.KillSwitch{
		Reason:         wire.KillSwitchDiscrepancyDetected,
		OperatorUserID: "01HKOPER00000000000000000A",
		Scope:          wire.KillSwitchScopeAll,
	}); err != nil {
		t.Fatalf("SendKillSwitch: %v", err)
	}
	// Wait for the kill ack so the latch is set before we dispatch.
	if !waitForCaps(t, &capMu, &caps, 1, 3*time.Second) {
		t.Fatalf("no kill ack within 3s")
	}

	// Dispatch an order after the kill — must be rejected, never filled.
	orders := []strategy.OrderIntent{{
		Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy,
		OrderType: strategy.OrderTypeMarket, QuantityUSD: 1000,
		ClientOrderID: postKillCOID,
		ValidUntilMs:  time.Now().UnixMilli() + 60_000,
	}}
	if err := hub.Dispatch(ctx, "01HKINSTANCE0000000000000A", accountID, "BTCUSDT", 50000.0, orders); err != nil {
		t.Fatalf("hub.Dispatch: %v", err)
	}
	if !waitForCaps(t, &capMu, &caps, 2, 3*time.Second) {
		t.Fatalf("no trade ack within 3s (got %d caps)", len(caps))
	}

	capMu.Lock()
	defer capMu.Unlock()
	var tradeAck *wire.Ack
	for _, c := range caps {
		if c.t != wire.TypeAck {
			continue
		}
		a, err := wire.DecodePayload[wire.Ack](c.env)
		if err != nil {
			t.Fatalf("decode ack: %v", err)
		}
		if a.ClientOrderID == postKillCOID {
			tradeAck = a
		}
	}
	if tradeAck == nil {
		t.Fatal("no ack for the post-kill trade_command")
	}
	if tradeAck.Status != wire.AckStatusRejected {
		t.Errorf("post-kill trade ack.status = %q, want rejected", tradeAck.Status)
	}
	if !strings.Contains(tradeAck.RejectReason, "frozen") {
		t.Errorf("reject reason = %q, want it to mention 'frozen'", tradeAck.RejectReason)
	}
}
