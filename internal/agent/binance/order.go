// order.go — Step 3 of the Binance adapter build. SubmitMarket captures
// the best-side reference price (ask for buy, bid for sell) via
// BookTicker, then POSTs /api/v3/order with type=MARKET and
// newOrderRespType=FULL so the response carries inline fills.
//
// MarketRef must be sampled BEFORE submission per
// docs/saas-ws-protocol-v1.md §8.2 — otherwise actual_slippage_bps
// would be tautological. If BookTicker fails the order is not sent;
// the caller sees a transport error and can retry.
package binance

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

// rawOrderResponse mirrors the FULL response shape of POST
// /api/v3/order. We only decode the fields the agent consumes;
// makerCommission, side, status, etc. are discarded.
type rawOrderResponse struct {
	OrderID      int64     `json:"orderId"`
	TransactTime int64     `json:"transactTime"`
	Status       string    `json:"status"`
	Fills        []rawFill `json:"fills"`
}

// rawFill is one entry in rawOrderResponse.Fills. Binance does not
// include a per-fill timestamp in the order response — all fills
// inherit transactTime — but it DOES carry a per-fill tradeId, which is
// the canonical dedup key (multiple fills of one sweep share transactTime,
// so the SaaS side must dedup on tradeId, not ms).
type rawFill struct {
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
	TradeID         int64  `json:"tradeId"`
}

// SubmitMarket places a MARKET order on Binance Spot and returns the
// agent-level result. The flow is:
//
//  1. Validate ExchangeOrder fields locally.
//  2. BookTicker(symbol) — capture MarketRef for ActualSlippageBps.
//  3. POST /api/v3/order with newOrderRespType=FULL.
//  4. Convert response fills → agent.ExchangeFill.
//
// Errors:
//   - Binance-side rejection (*APIError) is wrapped with
//     agent.ErrExchangeRejected so the wire-layer Ack carries
//     status=rejected unambiguously.
//   - Network / decode errors are returned raw; the caller treats
//     them as transport problems (order outcome is unknown).
func (c *Client) SubmitMarket(ctx context.Context, order agent.ExchangeOrder) (*agent.ExchangeSubmitResult, error) {
	if order.OrderType != "market" {
		return nil, fmt.Errorf("binance.SubmitMarket: order_type=%q, want market", order.OrderType)
	}
	if order.Symbol == "" {
		return nil, errors.New("binance.SubmitMarket: empty symbol")
	}
	if order.ClientOrderID == "" {
		return nil, errors.New("binance.SubmitMarket: empty client_order_id")
	}
	if order.Quantity.IsZero() || order.Quantity.IsNegative() {
		return nil, fmt.Errorf("binance.SubmitMarket: quantity=%s, must be positive", order.Quantity)
	}

	binSide, err := mapSide(order.Side)
	if err != nil {
		return nil, err
	}

	// MarketRef capture: ask for buy, bid for sell. Sampling before
	// the order ensures the reference is independent of the fill price.
	book, err := c.BookTicker(ctx, order.Symbol)
	if err != nil {
		return nil, fmt.Errorf("binance.SubmitMarket: capture MarketRef: %w", err)
	}
	var marketRef decimal.Decimal
	if binSide == "BUY" {
		marketRef = book.AskPrice
	} else {
		marketRef = book.BidPrice
	}

	// Snap the quantity onto the symbol's LOT_SIZE grid (and validate
	// minQty/minNotional against marketRef) so Binance doesn't reject it
	// with -1013. A below-floor order is rejected locally here.
	filter, err := c.symbolFilterFor(ctx, order.Symbol)
	if err != nil {
		return nil, fmt.Errorf("binance.SubmitMarket: %w", err)
	}
	order.Quantity, err = compliantQuantity(order.Quantity, marketRef, filter)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("side", binSide)
	params.Set("type", "MARKET")
	params.Set("quantity", order.Quantity.String())
	params.Set("newClientOrderId", order.ClientOrderID)
	// FULL: response includes the fills array; ACK / RESULT do not.
	params.Set("newOrderRespType", "FULL")

	body, err := c.signed(ctx, http.MethodPost, "/api/v3/order", params)
	if err != nil {
		return nil, wrapSubmitErr("binance.SubmitMarket", err)
	}

	var raw rawOrderResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance.SubmitMarket: decode response: %w", err)
	}
	if raw.OrderID == 0 {
		return nil, fmt.Errorf("binance.SubmitMarket: response missing orderId: %s", string(body))
	}

	fills, err := decodeFills("binance.SubmitMarket", raw.Fills, raw.TransactTime)
	if err != nil {
		return nil, err
	}

	return &agent.ExchangeSubmitResult{
		ExchangeOrderID: strconv.FormatInt(raw.OrderID, 10),
		AcceptedAtMs:    raw.TransactTime,
		MarketRef:       marketRef,
		Fills:           fills,
	}, nil
}

// wrapSubmitErr translates POST /api/v3/order failures into the
// agent-level taxonomy. RateLimitError must be checked before
// APIError because it unwraps to one. The order never reached the
// matching engine in either case, so callers see a stable
// agent.ErrExchangeRejected with a reason suffix.
func wrapSubmitErr(prefix string, err error) error {
	var rlErr *RateLimitError
	if errors.As(err, &rlErr) {
		reason := "rate_limited"
		if rlErr.Banned {
			reason = "ip_banned"
		}
		return fmt.Errorf("%w: %s retry_after=%s",
			agent.ErrExchangeRejected, reason, rlErr.RetryAfter)
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return fmt.Errorf("%w: %s", agent.ErrExchangeRejected, apiErr.Error())
	}
	return fmt.Errorf("%s: %w", prefix, err)
}

// decodeFills converts a rawOrderResponse.Fills slice into the
// agent-level shape. All three decimal fields must parse; a partial
// parse means the response is corrupt and the order outcome is
// unknown to the caller.
func decodeFills(prefix string, raw []rawFill, transactTime int64) ([]agent.ExchangeFill, error) {
	fills := make([]agent.ExchangeFill, 0, len(raw))
	for i, f := range raw {
		fillQty, err := decimal.NewFromString(f.Qty)
		if err != nil {
			return nil, fmt.Errorf("%s: fill[%d].qty %q: %w", prefix, i, f.Qty, err)
		}
		fillPrice, err := decimal.NewFromString(f.Price)
		if err != nil {
			return nil, fmt.Errorf("%s: fill[%d].price %q: %w", prefix, i, f.Price, err)
		}
		fillFee, err := decimal.NewFromString(f.Commission)
		if err != nil {
			return nil, fmt.Errorf("%s: fill[%d].commission %q: %w", prefix, i, f.Commission, err)
		}
		fills = append(fills, agent.ExchangeFill{
			FillQuantity:       fillQty,
			FillPrice:          fillPrice,
			FillFeeAsset:       f.CommissionAsset,
			FillFeeAmount:      fillFee,
			FilledAtExchangeMs: transactTime,
			TradeID:            f.TradeID,
		})
	}
	return fills, nil
}

// mapSide normalises agent's "buy" / "sell" to Binance's "BUY" /
// "SELL". Case-insensitive on input.
func mapSide(s string) (string, error) {
	switch strings.ToLower(s) {
	case "buy":
		return "BUY", nil
	case "sell":
		return "SELL", nil
	}
	return "", fmt.Errorf("binance: invalid side %q", s)
}

// SubmitLimit places a LIMIT GTC order on Binance Spot. The shape is
// identical to SubmitMarket except:
//
//  1. No BookTicker round-trip — protocol §5.10 fixes MarketRef =
//     LimitPrice for limit orders, so the snapshot isn't needed.
//  2. price + timeInForce=GTC are added to the params.
//  3. Inline fills are usually empty (status=NEW) — the order rests
//     on the book and asynchronous fills arrive via the User Data
//     Stream. A limit order that immediately crosses the book returns
//     status=FILLED with inline fills, decoded identically to
//     SubmitMarket.
func (c *Client) SubmitLimit(ctx context.Context, order agent.ExchangeOrder) (*agent.ExchangeSubmitResult, error) {
	if order.OrderType != "limit" {
		return nil, fmt.Errorf("binance.SubmitLimit: order_type=%q, want limit", order.OrderType)
	}
	if order.Symbol == "" {
		return nil, errors.New("binance.SubmitLimit: empty symbol")
	}
	if order.ClientOrderID == "" {
		return nil, errors.New("binance.SubmitLimit: empty client_order_id")
	}
	if order.Quantity.IsZero() || order.Quantity.IsNegative() {
		return nil, fmt.Errorf("binance.SubmitLimit: quantity=%s, must be positive", order.Quantity)
	}
	if order.LimitPrice.IsZero() || order.LimitPrice.IsNegative() {
		return nil, fmt.Errorf("binance.SubmitLimit: limit_price=%s, must be positive", order.LimitPrice)
	}

	binSide, err := mapSide(order.Side)
	if err != nil {
		return nil, err
	}

	// Snap price onto the PRICE_FILTER tick grid and quantity onto the
	// LOT_SIZE grid (notional checked against the limit price) before
	// submission, mirroring SubmitMarket's -1013 avoidance.
	filter, err := c.symbolFilterFor(ctx, order.Symbol)
	if err != nil {
		return nil, fmt.Errorf("binance.SubmitLimit: %w", err)
	}
	order.LimitPrice = compliantPrice(order.LimitPrice, filter)
	order.Quantity, err = compliantQuantity(order.Quantity, order.LimitPrice, filter)
	if err != nil {
		return nil, err
	}

	params := url.Values{}
	params.Set("symbol", order.Symbol)
	params.Set("side", binSide)
	params.Set("type", "LIMIT")
	// GTC = Good Till Cancel. IOC / FOK are out of scope for v1; the
	// protocol doesn't surface either to SaaS yet, so committing to GTC
	// keeps the wire contract one-dimensional.
	params.Set("timeInForce", "GTC")
	params.Set("quantity", order.Quantity.String())
	params.Set("price", order.LimitPrice.String())
	params.Set("newClientOrderId", order.ClientOrderID)
	params.Set("newOrderRespType", "FULL")

	body, err := c.signed(ctx, http.MethodPost, "/api/v3/order", params)
	if err != nil {
		return nil, wrapSubmitErr("binance.SubmitLimit", err)
	}

	var raw rawOrderResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance.SubmitLimit: decode response: %w", err)
	}
	if raw.OrderID == 0 {
		return nil, fmt.Errorf("binance.SubmitLimit: response missing orderId: %s", string(body))
	}

	fills, err := decodeFills("binance.SubmitLimit", raw.Fills, raw.TransactTime)
	if err != nil {
		return nil, err
	}

	return &agent.ExchangeSubmitResult{
		ExchangeOrderID: strconv.FormatInt(raw.OrderID, 10),
		AcceptedAtMs:    raw.TransactTime,
		// Protocol §5.10: limit-order slippage is computed against
		// LimitPrice, not best-side book.
		MarketRef: order.LimitPrice,
		Fills:     fills,
	}, nil
}
