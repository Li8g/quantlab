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
	ex, err := buildSpotExecution(ou, f)
	if err != nil {
		t.Fatalf("buildSpotExecution: %v", err)
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
	ou := &wire.OrderUpdate{ClientOrderID: "x"}
	_, err := buildSpotExecution(ou, wire.Fill{
		FillQuantityDecimal:  "not-a-number",
		FillPriceDecimal:     "1",
		FillFeeAmountDecimal: "0",
	})
	if err == nil {
		t.Fatal("want error on bad fill_quantity_decimal")
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
