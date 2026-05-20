package wire

// StateSyncRequest is sent by SaaS immediately after AuthOK (and after any
// reconnect). Payload empty; the Agent must reply within 1500ms (§4.2).
type StateSyncRequest struct{}

// Position is one row of the Agent's reported balance snapshot. Quantities
// stay as decimal strings across the wire (§2.2); Agent uses
// shopspring/decimal internally, SaaS converts to float64 for the ledger.
type Position struct {
	Symbol         string `json:"symbol"`
	FreeDecimal    string `json:"free_decimal"`
	LockedDecimal  string `json:"locked_decimal"`
}

// OpenOrder is one in-flight order known to the exchange at sync time. Used
// by SaaS to reconcile against TradeRecord; discrepancies log a
// discrepancy_event (§6.2 in the protocol doc).
type OpenOrder struct {
	ClientOrderID          string `json:"client_order_id"`
	ExchangeOrderID        string `json:"exchange_order_id"`
	Symbol                 string `json:"symbol"`
	Side                   string `json:"side"`
	OrderType              string `json:"order_type"`
	QuantityDecimal        string `json:"quantity_decimal"`
	FilledQuantityDecimal  string `json:"filled_quantity_decimal"`
	LimitPriceDecimal      string `json:"limit_price_decimal,omitempty"`
	Status                 string `json:"status"`
	PlacedAtMs             int64  `json:"placed_at_ms"`
}

// StateSyncResponse is the Agent's full snapshot — positions, open orders,
// and the fills the SaaS may have missed during the last disconnect.
// LastSeenMsgID is the last trade_command the Agent successfully decoded,
// letting SaaS replay the gap if it kept the outbox.
type StateSyncResponse struct {
	ReportedAtMs    int64     `json:"reported_at_ms"`
	Positions       []Position `json:"positions"`
	OpenOrders      []OpenOrder `json:"open_orders"`
	SinceLastFills  []Fill    `json:"since_last_fills"`
	LastSeenMsgID   string    `json:"last_seen_msg_id,omitempty"`
}
