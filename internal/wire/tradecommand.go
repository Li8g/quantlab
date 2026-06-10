package wire

// IntentKind tags whether the order originated from the macro or the micro
// engine. Mirrors strategy.OrderKind (engine-side enum) but stays a wire
// type to keep this package independent of internal/strategy.
type IntentKind string

const (
	IntentKindMacro IntentKind = "macro"
	IntentKindMicro IntentKind = "micro"
)

// time_in_force values carried on a limit TradeCommand. Absent (omitempty)
// ⇒ GTC, which is what a pre-B2 Agent assumed unconditionally — so the field
// is backward-compatible. B2 price protection dispatches marketable limits as
// IOC so an unfilled (flash-crossed) order leaves no resting order or locked
// balance (decision-b2-limit-order-price-protection.md D4).
const (
	TimeInForceGTC = "GTC"
	TimeInForceIOC = "IOC"
)

// TradeCommand is the SaaS-to-Agent order intent (§5.8).
//
// The OrderIntent → TradeCommand conversion happens in the SaaS dispatcher,
// not in this package: dispatcher reads OrderIntent.QuantityUSD (float64)
// and the latest_close_price for Symbol, converts to asset units, and
// renders QuantityDecimal as a string with 8 decimal places (BTC-grade).
//
// LimitPriceDecimal is omitempty: market orders leave it absent (not "0").
//
// TimeInForce is omitempty: absent ⇒ GTC (pre-B2 Agent behavior). Only set on
// limit orders; the dispatcher stamps IOC on the marketable limits it derives
// from market intents (B2 price protection).
type TradeCommand struct {
	IntentKind        IntentKind `json:"intent_kind"`
	ClientOrderID     string     `json:"client_order_id"`
	InstanceID        string     `json:"instance_id"`
	Symbol            string     `json:"symbol"`
	Side              string     `json:"side"`
	OrderType         string     `json:"order_type"`
	QuantityDecimal   string     `json:"quantity_decimal"`
	LimitPriceDecimal string     `json:"limit_price_decimal,omitempty"`
	TimeInForce       string     `json:"time_in_force,omitempty"`
	ValidUntilMs      int64      `json:"valid_until_ms"`
	NowMsAtSaaS       int64      `json:"now_ms_at_saas"`
}
