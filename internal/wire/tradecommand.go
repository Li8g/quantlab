package wire

// IntentKind tags whether the order originated from the macro or the micro
// engine. Mirrors strategy.OrderKind (engine-side enum) but stays a wire
// type to keep this package independent of internal/strategy.
type IntentKind string

const (
	IntentKindMacro IntentKind = "macro"
	IntentKindMicro IntentKind = "micro"
)

// TradeCommand is the SaaS-to-Agent order intent (§5.8).
//
// The OrderIntent → TradeCommand conversion happens in the SaaS dispatcher,
// not in this package: dispatcher reads OrderIntent.QuantityUSD (float64)
// and the latest_close_price for Symbol, converts to asset units, and
// renders QuantityDecimal as a string with 8 decimal places (BTC-grade).
//
// LimitPriceDecimal is omitempty: market orders leave it absent (not "0").
type TradeCommand struct {
	IntentKind        IntentKind `json:"intent_kind"`
	ClientOrderID     string     `json:"client_order_id"`
	InstanceID        string     `json:"instance_id"`
	Symbol            string     `json:"symbol"`
	Side              string     `json:"side"`
	OrderType         string     `json:"order_type"`
	QuantityDecimal   string     `json:"quantity_decimal"`
	LimitPriceDecimal string     `json:"limit_price_decimal,omitempty"`
	ValidUntilMs      int64      `json:"valid_until_ms"`
	NowMsAtSaaS       int64      `json:"now_ms_at_saas"`
}
