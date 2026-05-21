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
// inherit transactTime.
type rawFill struct {
	Price           string `json:"price"`
	Qty             string `json:"qty"`
	Commission      string `json:"commission"`
	CommissionAsset string `json:"commissionAsset"`
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
		// Check RateLimitError BEFORE APIError — RateLimitError also
		// satisfies errors.As(*APIError) via Unwrap, so the order is
		// important. The order never reached the matching engine
		// (Binance throttles before evaluation) so we surface it as a
		// rejection with a stable reason, preserving retry hints in
		// the message for ops/diagnostic.
		var rlErr *RateLimitError
		if errors.As(err, &rlErr) {
			reason := "rate_limited"
			if rlErr.Banned {
				reason = "ip_banned"
			}
			return nil, fmt.Errorf("%w: %s retry_after=%s",
				agent.ErrExchangeRejected, reason, rlErr.RetryAfter)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("%w: %s", agent.ErrExchangeRejected, apiErr.Error())
		}
		return nil, fmt.Errorf("binance.SubmitMarket: %w", err)
	}

	var raw rawOrderResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance.SubmitMarket: decode response: %w", err)
	}
	if raw.OrderID == 0 {
		return nil, fmt.Errorf("binance.SubmitMarket: response missing orderId: %s", string(body))
	}

	fills := make([]agent.ExchangeFill, 0, len(raw.Fills))
	for i, f := range raw.Fills {
		fillQty, err := decimal.NewFromString(f.Qty)
		if err != nil {
			return nil, fmt.Errorf("binance.SubmitMarket: fill[%d].qty %q: %w", i, f.Qty, err)
		}
		fillPrice, err := decimal.NewFromString(f.Price)
		if err != nil {
			return nil, fmt.Errorf("binance.SubmitMarket: fill[%d].price %q: %w", i, f.Price, err)
		}
		fillFee, err := decimal.NewFromString(f.Commission)
		if err != nil {
			return nil, fmt.Errorf("binance.SubmitMarket: fill[%d].commission %q: %w", i, f.Commission, err)
		}
		fills = append(fills, agent.ExchangeFill{
			FillQuantity:       fillQty,
			FillPrice:          fillPrice,
			FillFeeAsset:       f.CommissionAsset,
			FillFeeAmount:      fillFee,
			FilledAtExchangeMs: raw.TransactTime,
		})
	}

	return &agent.ExchangeSubmitResult{
		ExchangeOrderID: strconv.FormatInt(raw.OrderID, 10),
		AcceptedAtMs:    raw.TransactTime,
		MarketRef:       marketRef,
		Fills:           fills,
	}, nil
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
//     Stream (Phase 7.11). A limit order that immediately crosses the
//     book returns status=FILLED with inline fills, which we decode
//     identically to SubmitMarket.
//
// Until the User Data Stream wiring lands (7.11), a non-crossing
// limit order is essentially fire-and-forget — Agent surfaces Ack
// {status=accepted, exchange_order_id, market_ref=limit_price} and
// no subsequent OrderUpdate. Acceptable interim state for testnet /
// paper-trading use.
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
		// Same error-class layering as SubmitMarket — RateLimitError
		// must be checked before APIError because it Unwraps to one.
		var rlErr *RateLimitError
		if errors.As(err, &rlErr) {
			reason := "rate_limited"
			if rlErr.Banned {
				reason = "ip_banned"
			}
			return nil, fmt.Errorf("%w: %s retry_after=%s",
				agent.ErrExchangeRejected, reason, rlErr.RetryAfter)
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			return nil, fmt.Errorf("%w: %s", agent.ErrExchangeRejected, apiErr.Error())
		}
		return nil, fmt.Errorf("binance.SubmitLimit: %w", err)
	}

	var raw rawOrderResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("binance.SubmitLimit: decode response: %w", err)
	}
	if raw.OrderID == 0 {
		return nil, fmt.Errorf("binance.SubmitLimit: response missing orderId: %s", string(body))
	}

	fills := make([]agent.ExchangeFill, 0, len(raw.Fills))
	for i, f := range raw.Fills {
		fillQty, err := decimal.NewFromString(f.Qty)
		if err != nil {
			return nil, fmt.Errorf("binance.SubmitLimit: fill[%d].qty %q: %w", i, f.Qty, err)
		}
		fillPrice, err := decimal.NewFromString(f.Price)
		if err != nil {
			return nil, fmt.Errorf("binance.SubmitLimit: fill[%d].price %q: %w", i, f.Price, err)
		}
		fillFee, err := decimal.NewFromString(f.Commission)
		if err != nil {
			return nil, fmt.Errorf("binance.SubmitLimit: fill[%d].commission %q: %w", i, f.Commission, err)
		}
		fills = append(fills, agent.ExchangeFill{
			FillQuantity:       fillQty,
			FillPrice:          fillPrice,
			FillFeeAsset:       f.CommissionAsset,
			FillFeeAmount:      fillFee,
			FilledAtExchangeMs: raw.TransactTime,
		})
	}

	return &agent.ExchangeSubmitResult{
		ExchangeOrderID: strconv.FormatInt(raw.OrderID, 10),
		AcceptedAtMs:    raw.TransactTime,
		// Protocol §5.10: limit-order slippage is computed against
		// LimitPrice, not best-side book — keep the field populated so
		// downstream sees a consistent non-zero MarketRef for both
		// order types.
		MarketRef: order.LimitPrice,
		Fills:     fills,
	}, nil
}
