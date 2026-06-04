package main

import (
	"testing"

	"quantlab/internal/saas/store"
	"quantlab/internal/strategy"
	"quantlab/internal/wire"
)

func TestAckToTradeStatus(t *testing.T) {
	cases := []struct {
		name       string
		in         wire.AckStatus
		wantStatus store.TradeStatus
		wantOK     bool
	}{
		{"accepted stays pending", wire.AckStatusAccepted, "", false},
		{"rejected", wire.AckStatusRejected, store.TradeStatusRejected, true},
		{"expired → cancelled", wire.AckStatusExpired, store.TradeStatusCancelled, true},
		{"duplicate_pending stays pending", wire.AckStatusDuplicatePending, "", false},
		{"duplicate_terminal no-op", wire.AckStatusDuplicateTerminal, "", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := ackToTradeStatus(c.in)
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
			}
			if got != c.wantStatus {
				t.Errorf("status = %q, want %q", got, c.wantStatus)
			}
		})
	}
}

func TestOrderUpdateToTradeStatus(t *testing.T) {
	cases := []struct {
		in   wire.OrderStatus
		want store.TradeStatus
	}{
		{wire.OrderStatusFilled, store.TradeStatusFilled},
		{wire.OrderStatusPartialFilled, store.TradeStatusPartialFilled},
		{wire.OrderStatusCancelled, store.TradeStatusCancelled},
		{wire.OrderStatusRejected, store.TradeStatusRejected},
	}
	for _, c := range cases {
		got, ok := orderUpdateToTradeStatus(c.in)
		if !ok {
			t.Errorf("status %q: ok=false, want true", c.in)
		}
		if got != c.want {
			t.Errorf("status %q: got %q, want %q", c.in, got, c.want)
		}
	}
	if _, ok := orderUpdateToTradeStatus(wire.OrderStatus("bogus")); ok {
		t.Errorf("unknown status returned ok=true")
	}
}

func TestBuildSpotExecution_DecimalParsing(t *testing.T) {
	ou := &wire.OrderUpdate{
		ClientOrderID:   "01HKCOID000000000000000001",
		ExchangeOrderID: "EX-42",
		Status:          wire.OrderStatusFilled,
	}
	f := wire.Fill{
		FillQuantityDecimal:  "0.01923077",
		FillPriceDecimal:     "52000.50",
		FillFeeAsset:         "USDT",
		FillFeeAmountDecimal: "1.000000",
		FilledAtExchangeMs:   1714000000000,
		ActualSlippageBps:    2.5,
	}
	ex, err := buildSpotExecutionFrom(ou.ClientOrderID, ou.ExchangeOrderID, f)
	if err != nil {
		t.Fatalf("buildSpotExecutionFrom: %v", err)
	}
	if ex.ClientOrderID != "01HKCOID000000000000000001" {
		t.Errorf("ClientOrderID = %q", ex.ClientOrderID)
	}
	if ex.ExchangeOrderID != "EX-42" {
		t.Errorf("ExchangeOrderID = %q", ex.ExchangeOrderID)
	}
	if got := ex.FillQuantity; got < 0.01923076 || got > 0.01923078 {
		t.Errorf("FillQuantity = %v", got)
	}
	if ex.FillPrice != 52000.50 {
		t.Errorf("FillPrice = %v", ex.FillPrice)
	}
	if ex.FillFeeAmount != 1.0 {
		t.Errorf("FillFeeAmount = %v", ex.FillFeeAmount)
	}
	if ex.ActualSlippageBPS != 2.5 {
		t.Errorf("ActualSlippageBPS = %v", ex.ActualSlippageBPS)
	}
	if ex.FilledAtExchangeMs != 1714000000000 {
		t.Errorf("FilledAtExchangeMs = %v", ex.FilledAtExchangeMs)
	}
}

func TestBuildSpotExecution_BadDecimal(t *testing.T) {
	_, err := buildSpotExecutionFrom("x", "", wire.Fill{
		FillQuantityDecimal:  "not-a-number",
		FillPriceDecimal:     "1",
		FillFeeAmountDecimal: "0",
	})
	if err == nil {
		t.Fatal("want error on bad fill_quantity_decimal")
	}
}

// delta_report fills carry their own order ids (unlike order_update);
// buildSpotExecutionFrom must take them from the args, not a parent.
func TestBuildSpotExecutionFrom_DeltaReportFill(t *testing.T) {
	f := wire.Fill{
		FillQuantityDecimal:  "0.5",
		FillPriceDecimal:     "60000",
		FillFeeAsset:         "USDT",
		FillFeeAmountDecimal: "0.3",
		FilledAtExchangeMs:   1714000000999,
		ActualSlippageBps:    1.0,
		ClientOrderID:        "dr-co-1",
		ExchangeOrderID:      "dr-ex-1",
	}
	ex, err := buildSpotExecutionFrom(f.ClientOrderID, f.ExchangeOrderID, f)
	if err != nil {
		t.Fatalf("buildSpotExecutionFrom: %v", err)
	}
	if ex.ClientOrderID != "dr-co-1" || ex.ExchangeOrderID != "dr-ex-1" {
		t.Errorf("order ids = %q/%q, want dr-co-1/dr-ex-1", ex.ClientOrderID, ex.ExchangeOrderID)
	}
	if ex.FillQuantity != 0.5 || ex.FilledAtExchangeMs != 1714000000999 {
		t.Errorf("fill = %+v", ex)
	}
}

func TestReconcilePositions(t *testing.T) {
	// flaggedSet returns the assets reconcilePositions flagged as drift.
	flaggedSet := func(t *testing.T, expected map[string]float64, pos []wire.Position) map[string]bool {
		t.Helper()
		drifts, err := reconcilePositions(expected, pos)
		if err != nil {
			t.Fatalf("reconcilePositions: %v", err)
		}
		got := map[string]bool{}
		for _, d := range drifts {
			if d.Flagged {
				got[d.Asset] = true
			}
		}
		return got
	}
	pos := func(sym, free, locked string) wire.Position {
		return wire.Position{Symbol: sym, FreeDecimal: free, LockedDecimal: locked}
	}
	eq := func(a, b map[string]bool) bool {
		if len(a) != len(b) {
			return false
		}
		for k := range a {
			if !b[k] {
				return false
			}
		}
		return true
	}

	cases := []struct {
		name     string
		expected map[string]float64
		pos      []wire.Position
		want     map[string]bool // assets expected to be flagged
	}{
		{
			name:     "matched holdings: no drift",
			expected: map[string]float64{"BTC": 0.5, "USDT": 1000},
			pos:      []wire.Position{pos("BTC", "0.5", "0.0"), pos("USDT", "1000.0", "0.0")},
			want:     map[string]bool{},
		},
		{
			name:     "free+locked summed to match",
			expected: map[string]float64{"BTC": 0.5},
			pos:      []wire.Position{pos("BTC", "0.3", "0.2")}, // 0.3+0.2 == 0.5
			want:     map[string]bool{},
		},
		{
			name:     "BTC drift flagged",
			expected: map[string]float64{"BTC": 0.5, "USDT": 1000},
			pos:      []wire.Position{pos("BTC", "0.6", "0.0"), pos("USDT", "1000.0", "0.0")},
			want:     map[string]bool{"BTC": true},
		},
		{
			name:     "BTC dust under floor: not flagged",
			expected: map[string]float64{"BTC": 0},
			pos:      []wire.Position{pos("BTC", "0.0000005", "0.0")}, // 5e-7 < 1e-6 floor
			want:     map[string]bool{},
		},
		{
			name:     "USDT dust under floor: not flagged",
			expected: map[string]float64{"USDT": 0},
			pos:      []wire.Position{pos("USDT", "0.005", "0.0")}, // < 0.01 floor
			want:     map[string]bool{},
		},
		{
			name:     "exchange holds asset SaaS doesn't track",
			expected: map[string]float64{},
			pos:      []wire.Position{pos("ETH", "2.0", "0.0")},
			want:     map[string]bool{"ETH": true},
		},
		{
			name:     "SaaS expects asset exchange doesn't report",
			expected: map[string]float64{"BTC": 1.0},
			pos:      []wire.Position{},
			want:     map[string]bool{"BTC": true},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := flaggedSet(t, c.expected, c.pos)
			if !eq(got, c.want) {
				t.Errorf("flagged = %v, want %v", got, c.want)
			}
		})
	}
}

func TestReconcilePositions_DeterministicOrderAndDiffSign(t *testing.T) {
	drifts, err := reconcilePositions(
		map[string]float64{"USDT": 1000, "BTC": 0.5},
		[]wire.Position{
			{Symbol: "USDT", FreeDecimal: "1100", LockedDecimal: "0"}, // +100
			{Symbol: "BTC", FreeDecimal: "0.4", LockedDecimal: "0"},   // -0.1
		},
	)
	if err != nil {
		t.Fatalf("reconcilePositions: %v", err)
	}
	// Sorted asset order: BTC, USDT.
	if len(drifts) != 2 || drifts[0].Asset != "BTC" || drifts[1].Asset != "USDT" {
		t.Fatalf("order = %+v, want [BTC, USDT]", drifts)
	}
	if drifts[0].Diff >= 0 {
		t.Errorf("BTC diff = %v, want negative (actual < expected)", drifts[0].Diff)
	}
	if drifts[1].Diff <= 0 {
		t.Errorf("USDT diff = %v, want positive (actual > expected)", drifts[1].Diff)
	}
}

func TestReconcilePositions_BadDecimal(t *testing.T) {
	_, err := reconcilePositions(
		map[string]float64{"BTC": 1},
		[]wire.Position{{Symbol: "BTC", FreeDecimal: "abc", LockedDecimal: "0"}},
	)
	if err == nil {
		t.Fatal("want error on bad free_decimal")
	}
}

func TestBuildSeedPortfolio_AnchorsBaseAndUSDT(t *testing.T) {
	inst := &store.StrategyInstance{InstanceID: "inst1", Pair: "BTCUSDT"}
	actual := map[string]float64{
		"BTC":  0.5,
		"USDT": 1000,
		"ACH":  9999, // faucet junk — must NOT seed the ledger
	}
	seed := buildSeedPortfolio(inst, actual, 42)

	if seed.InstanceID != "inst1" || seed.NowMs != 42 {
		t.Fatalf("identity = %+v, want inst1/42", seed)
	}
	if seed.FloatBTC != 0.5 {
		t.Errorf("FloatBTC = %v, want 0.5 (whole base balance into active float)", seed.FloatBTC)
	}
	if seed.USDT != 1000 {
		t.Errorf("USDT = %v, want 1000", seed.USDT)
	}
	// Genesis: dead/cold/last-bar all zero; junk coins never enter the ledger.
	if seed.DeadBTC != 0 || seed.ColdSealedBTC != 0 || seed.LastProcessedBarTime != 0 {
		t.Errorf("genesis non-zero: dead=%v cold=%v lastBar=%v", seed.DeadBTC, seed.ColdSealedBTC, seed.LastProcessedBarTime)
	}
}

func TestBuildSeedPortfolio_MissingPositionsSeedZero(t *testing.T) {
	inst := &store.StrategyInstance{InstanceID: "inst2", Pair: "ETHUSDT"}
	seed := buildSeedPortfolio(inst, map[string]float64{}, 7)
	if seed.FloatBTC != 0 || seed.USDT != 0 {
		t.Errorf("absent assets must seed zero, got float=%v usdt=%v", seed.FloatBTC, seed.USDT)
	}
}

func TestBuildTradeRecord_LimitOrderCopiesPrice(t *testing.T) {
	oi := strategy.OrderIntent{
		Kind:          strategy.OrderKindMacro,
		Side:          strategy.OrderSideBuy,
		OrderType:     strategy.OrderTypeLimit,
		QuantityUSD:   1000,
		LimitPrice:    50000,
		ClientOrderID: "01HKCOID000000000000000007",
		ValidUntilMs:  1714000000000,
	}
	tr := buildTradeRecord("01HKINST00000000000000000A", "BTCUSDT", oi)
	if tr.Status != store.TradeStatusPending {
		t.Errorf("Status = %q, want pending", tr.Status)
	}
	if tr.LimitPrice == nil || *tr.LimitPrice != 50000 {
		t.Errorf("LimitPrice mismatch: %v", tr.LimitPrice)
	}
	if tr.Symbol != "BTCUSDT" || tr.InstanceID != "01HKINST00000000000000000A" {
		t.Errorf("Symbol/InstanceID mismatch: %+v", tr)
	}
	if tr.OrderType != "limit" || tr.Side != "buy" {
		t.Errorf("OrderType/Side mismatch: %+v", tr)
	}
}

func TestBuildTradeRecord_MarketOrderLeavesLimitNil(t *testing.T) {
	oi := strategy.OrderIntent{
		OrderType:     strategy.OrderTypeMarket,
		QuantityUSD:   500,
		ClientOrderID: "01HKCOID000000000000000008",
	}
	tr := buildTradeRecord("inst", "BTCUSDT", oi)
	if tr.LimitPrice != nil {
		t.Errorf("LimitPrice = %v, want nil for market order", tr.LimitPrice)
	}
}
