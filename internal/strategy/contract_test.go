package strategy

import (
	"encoding/json"
	"testing"
)

// TestOrderIntentRoundTrip verifies OrderIntent survives JSON marshal /
// unmarshal so the dispatcher can hand it to wire.TradeCommand on the
// Step → SaaS → Agent path (docs/saas-ws-protocol-v1.md §5.8).
func TestOrderIntentRoundTrip(t *testing.T) {
	in := OrderIntent{
		Kind:          OrderKindMicro,
		Side:          OrderSideBuy,
		OrderType:     OrderTypeLimit,
		QuantityUSD:   1000.0,
		LimitPrice:    50000.0,
		ClientOrderID: "demo-1",
		ValidUntilMs:  1700000060000,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var out OrderIntent
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if in != out {
		t.Errorf("round trip diff:\n  in =%+v\n  out=%+v", in, out)
	}
}

// TestOrderIntentOmitsLimitPriceForMarket verifies omitempty on LimitPrice
// — market orders should not carry a zero limit_price key.
func TestOrderIntentOmitsLimitPriceForMarket(t *testing.T) {
	in := OrderIntent{
		Kind:          OrderKindMacro,
		Side:          OrderSideBuy,
		OrderType:     OrderTypeMarket,
		QuantityUSD:   500.0,
		ClientOrderID: "demo-2",
		ValidUntilMs:  1700000060000,
	}
	b, err := json.Marshal(in)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := m["limit_price"]; ok {
		t.Errorf("market order should omit limit_price; got %s", string(b))
	}
}
