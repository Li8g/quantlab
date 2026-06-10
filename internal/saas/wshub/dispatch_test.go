package wshub

import (
	"context"
	"errors"
	"testing"
	"time"

	"quantlab/internal/strategy"
	"quantlab/internal/wire"
)

func TestDispatch_NoConnection(t *testing.T) {
	hub, _, _, _ := newTestHub(t)
	err := hub.Dispatch(context.Background(), "01HKINSTANCE0000000000000A", "01HKACCT00000000000000000A", "BTCUSDT", 50000.0,
		[]strategy.OrderIntent{{
			Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy, OrderType: strategy.OrderTypeMarket,
			QuantityUSD: 1000, ClientOrderID: "01HKCOID000000000000000000",
			ValidUntilMs: time.Now().UnixMilli() + 60000,
		}})
	if !errors.Is(err, ErrAccountNotConnected) {
		t.Errorf("got %v, want ErrAccountNotConnected", err)
	}
}

func TestDispatch_EmptyOrdersIsNoop(t *testing.T) {
	hub, _, _, _ := newTestHub(t)
	if err := hub.Dispatch(context.Background(), "i", "a", "BTCUSDT", 50000.0, nil); err != nil {
		t.Errorf("Dispatch empty orders: %v", err)
	}
}

func TestDispatch_HappyPath(t *testing.T) {
	hub, _, token, accountID := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	driveHandshake(t, pc, accountID, token)
	waitReady(t, hub, accountID)

	orders := []strategy.OrderIntent{
		{
			Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy, OrderType: strategy.OrderTypeMarket,
			QuantityUSD: 1000, ClientOrderID: "01HKCOID000000000000000001",
			ValidUntilMs: time.Now().UnixMilli() + 60000,
		},
		{
			Kind: strategy.OrderKindMicro, Side: strategy.OrderSideSell, OrderType: strategy.OrderTypeLimit,
			QuantityUSD: 500, LimitPrice: 50100.0,
			ClientOrderID: "01HKCOID000000000000000002",
			ValidUntilMs:  time.Now().UnixMilli() + 60000,
		},
	}
	if err := hub.Dispatch(context.Background(), "01HKINSTANCE0000000000000A", accountID, "BTCUSDT", 50000.0, orders); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	// Expect two trade_command frames.
	got1 := pc.clientReadEnv(t)
	if got1.Type != wire.TypeTradeCommand {
		t.Fatalf("expected trade_command, got %q", got1.Type)
	}
	tc1, _ := wire.DecodePayload[wire.TradeCommand](got1)
	if tc1.ClientOrderID != "01HKCOID000000000000000001" {
		t.Errorf("first cmd client_order_id = %q", tc1.ClientOrderID)
	}
	if tc1.QuantityDecimal != "0.02000000" {
		t.Errorf("first cmd qty = %q, want 0.02000000 (1000/50000)", tc1.QuantityDecimal)
	}
	if tc1.LimitPriceDecimal != "" {
		t.Errorf("market cmd has limit_price_decimal = %q", tc1.LimitPriceDecimal)
	}

	got2 := pc.clientReadEnv(t)
	if got2.Type != wire.TypeTradeCommand {
		t.Fatalf("expected trade_command, got %q", got2.Type)
	}
	tc2, _ := wire.DecodePayload[wire.TradeCommand](got2)
	if tc2.LimitPriceDecimal != "50100.00000000" {
		t.Errorf("limit cmd price = %q", tc2.LimitPriceDecimal)
	}
}

func TestDispatch_PreReadyReturnsNotConnected(t *testing.T) {
	hub, _, token, accountID := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	// Drive only the first half of the handshake; stop before
	// state_sync_response so the conn stays in phaseAuthed.
	pc.clientSend(t, encodeForClient(t, wire.TypeHello, accountID, wire.Hello{
		AccountID: accountID, SchemaVersion: wire.SchemaVersion,
	}))
	_ = pc.clientReadEnv(t) // auth_required
	pc.clientSend(t, encodeForClient(t, wire.TypeAuth, accountID, wire.Auth{Token: token}))
	_ = pc.clientReadEnv(t) // auth_ok
	_ = pc.clientReadEnv(t) // state_sync_request

	// At this point connection is registered (in phaseAuthed) but not Ready.
	// Wait briefly for Register to land.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if _, err := hub.Registry().Get(accountID); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	err := hub.Dispatch(context.Background(), "i", accountID, "BTCUSDT", 50000.0, []strategy.OrderIntent{{
		Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy, OrderType: strategy.OrderTypeMarket,
		QuantityUSD: 1000, ClientOrderID: "01HKCOID0000000000000PRE99",
		ValidUntilMs: time.Now().UnixMilli() + 60000,
	}})
	if !errors.Is(err, ErrAccountNotConnected) {
		t.Errorf("got %v, want ErrAccountNotConnected (pre-Ready)", err)
	}
}

func TestDispatch_InvalidLatestClose(t *testing.T) {
	hub, _, token, accountID := newTestHub(t)
	pc := newPipeConn()
	cancel, wg := runConnInBg(hub, pc)
	defer func() { cancel(); _ = pc.Close(); wg.Wait() }()

	driveHandshake(t, pc, accountID, token)
	waitReady(t, hub, accountID)

	err := hub.Dispatch(context.Background(), "i", accountID, "BTCUSDT", 0, []strategy.OrderIntent{{
		Kind: strategy.OrderKindMacro, Side: strategy.OrderSideBuy, OrderType: strategy.OrderTypeMarket,
		QuantityUSD: 1000, ClientOrderID: "01HKCOID0000000000000ZERO9",
		ValidUntilMs: time.Now().UnixMilli() + 60000,
	}})
	if err == nil || !contains(err.Error(), "invalid latestClose") {
		t.Errorf("got %v, want invalid latestClose error", err)
	}
}

// TestBuildTradeCommand_PriceCap exercises the B2 marketable-limit IOC
// conversion in isolation (buildTradeCommand is the dispatch-layer guardrail;
// decision-b2-limit-order-price-protection.md §4.5).
func TestBuildTradeCommand_PriceCap(t *testing.T) {
	const close = 50000.0
	mkt := func(side strategy.OrderSide) strategy.OrderIntent {
		return strategy.OrderIntent{
			Kind: strategy.OrderKindMacro, Side: side, OrderType: strategy.OrderTypeMarket,
			QuantityUSD: 1000, ClientOrderID: "01HKCOID000000000000000001",
		}
	}

	t.Run("cap_disabled_passes_market_through", func(t *testing.T) {
		tc, err := buildTradeCommand(mkt(strategy.OrderSideBuy), "i", "BTCUSDT", close, 0, 0)
		if err != nil {
			t.Fatal(err)
		}
		if tc.OrderType != "market" || tc.LimitPriceDecimal != "" || tc.TimeInForce != "" {
			t.Errorf("cap=0: got type=%q limit=%q tif=%q, want market/empty/empty",
				tc.OrderType, tc.LimitPriceDecimal, tc.TimeInForce)
		}
	})

	t.Run("buy_caps_above_close", func(t *testing.T) {
		tc, err := buildTradeCommand(mkt(strategy.OrderSideBuy), "i", "BTCUSDT", close, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		// 50000 × (1 + 50/1e4) = 50250.
		if tc.OrderType != "limit" || tc.LimitPriceDecimal != "50250.00000000" || tc.TimeInForce != wire.TimeInForceIOC {
			t.Errorf("buy cap=50: got type=%q limit=%q tif=%q, want limit/50250.00000000/IOC",
				tc.OrderType, tc.LimitPriceDecimal, tc.TimeInForce)
		}
	})

	t.Run("sell_caps_below_close", func(t *testing.T) {
		tc, err := buildTradeCommand(mkt(strategy.OrderSideSell), "i", "BTCUSDT", close, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		// 50000 × (1 − 50/1e4) = 49750.
		if tc.LimitPriceDecimal != "49750.00000000" || tc.TimeInForce != wire.TimeInForceIOC {
			t.Errorf("sell cap=50: got limit=%q tif=%q, want 49750.00000000/IOC",
				tc.LimitPriceDecimal, tc.TimeInForce)
		}
	})

	t.Run("strategy_limit_order_untouched", func(t *testing.T) {
		oi := strategy.OrderIntent{
			Kind: strategy.OrderKindMicro, Side: strategy.OrderSideSell, OrderType: strategy.OrderTypeLimit,
			QuantityUSD: 500, LimitPrice: 51000, ClientOrderID: "01HKCOID000000000000000002",
		}
		tc, err := buildTradeCommand(oi, "i", "BTCUSDT", close, 0, 50)
		if err != nil {
			t.Fatal(err)
		}
		// A strategy-chosen limit keeps its own price and stays GTC (no IOC stamp).
		if tc.LimitPriceDecimal != "51000.00000000" || tc.TimeInForce != "" {
			t.Errorf("strategy limit: got limit=%q tif=%q, want 51000.00000000/empty",
				tc.LimitPriceDecimal, tc.TimeInForce)
		}
	})
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
