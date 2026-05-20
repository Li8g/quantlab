package wire

// OrderStatus is the lifecycle state reported by the Agent on each fill
// event. Mirrors store.TradeStatus values relevant on the wire (pending /
// acked are SaaS-internal and never appear here).
type OrderStatus string

const (
	OrderStatusFilled        OrderStatus = "filled"
	OrderStatusPartialFilled OrderStatus = "partial_filled"
	OrderStatusCancelled     OrderStatus = "cancelled"
	OrderStatusRejected      OrderStatus = "rejected"
)

// Fill is one exchange-confirmed execution. ActualSlippageBps is a float64
// (basis points; §2.2 numeric exception): the precision loss is intentional
// — bps is already a derived quantity.
//
// Slippage reference (Agent-side computation, §8.2):
//   - market: (fill_price - market_ref_at_submit) / market_ref_at_submit × 10000
//   - limit:  (fill_price - limit_price) / limit_price × 10000
//
// For sells the sign convention flips: positive bps = worse than reference.
type Fill struct {
	FillQuantityDecimal   string  `json:"fill_quantity_decimal"`
	FillPriceDecimal      string  `json:"fill_price_decimal"`
	FillFeeAsset          string  `json:"fill_fee_asset"`
	FillFeeAmountDecimal  string  `json:"fill_fee_amount_decimal"`
	FilledAtExchangeMs    int64   `json:"filled_at_exchange_ms"`
	ActualSlippageBps     float64 `json:"actual_slippage_bps"`

	// ClientOrderID/ExchangeOrderID are only populated in delta_report
	// since_last_fills, where individual fills need to be associated
	// back to their order. In order_update.fills they are omitted because
	// the enclosing OrderUpdate already names the order.
	ClientOrderID   string `json:"client_order_id,omitempty"`
	ExchangeOrderID string `json:"exchange_order_id,omitempty"`
}

// OrderUpdate is the per-order lifecycle event from the Agent (§5.10).
// At least one Fill on partial_filled / filled; zero Fills on cancelled /
// rejected.
type OrderUpdate struct {
	ClientOrderID                    string      `json:"client_order_id"`
	ExchangeOrderID                  string      `json:"exchange_order_id"`
	Status                           OrderStatus `json:"status"`
	Fills                            []Fill      `json:"fills"`
	CumulativeFilledQuantityDecimal  string      `json:"cumulative_filled_quantity_decimal"`
}
