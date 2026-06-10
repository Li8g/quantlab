package agent

import (
	"context"
	"fmt"

	"github.com/shopspring/decimal"

	"quantlab/internal/wire"
	"quantlab/internal/wsconn"
)

// handleTradeCommand is the heart of the Agent: take an incoming trade
// command, dedupe by client_order_id, submit to the exchange, ack, and
// fire OrderUpdate for fills.
func (c *Client) handleTradeCommand(ctx context.Context, conn wsconn.Conn, env wire.Envelope) {
	tc, err := wire.DecodePayload[wire.TradeCommand](env)
	if err != nil {
		c.sendError(ctx, conn, wire.ErrorCodeDecodeFailed, err.Error(), env.MsgID)
		return
	}

	// Idempotency check (§2.5) — must come first, before frozen/expiry:
	// a known client_order_id always returns duplicate_pending/terminal
	// regardless of latch or expiry state (S5B-1). A read error is
	// fatal: fail-closed to prevent duplicate exchange submissions (S5B-2).
	existing, ok, idemErr := c.idempotency.Get(tc.ClientOrderID)
	if idemErr != nil {
		c.sendError(ctx, conn, wire.ErrorCodeInternalError, "idempotency read: "+idemErr.Error(), env.MsgID)
		return
	}
	if ok {
		c.sendAck(ctx, conn, c.duplicateAck(existing, c.nowMs()))
		return
	}

	// Frozen latch (kill_switch / HALTED): reject every new order without
	// submitting. Checked after idempotency so replays of known orders
	// always return a deterministic duplicate_* response. The latch
	// survives reconnect and clears only on process restart (§5.13).
	if c.frozen.Load() {
		c.sendAck(ctx, conn, wire.Ack{
			ClientOrderID: tc.ClientOrderID,
			Status:        wire.AckStatusRejected,
			ExchangeNowMs: c.nowMs(),
			RejectReason:  "agent frozen by kill_switch",
		})
		return
	}

	// Expiry check (§5.8): if valid_until_ms has passed (per Agent wall
	// clock OR per SaaS's NowMsAtSaaS, whichever is later), reject with
	// status=expired without submitting. Checked after idempotency so a
	// replayed expired command returns duplicate_* (not expired).
	clk := c.nowMs()
	saasClk := tc.NowMsAtSaaS
	wallClk := clk
	if saasClk > wallClk {
		wallClk = saasClk
	}
	if tc.ValidUntilMs > 0 && wallClk > tc.ValidUntilMs {
		c.sendAck(ctx, conn, wire.Ack{
			ClientOrderID: tc.ClientOrderID,
			Status:        wire.AckStatusExpired,
			ExchangeNowMs: clk,
			RejectReason:  "valid_until_ms passed before submit",
		})
		return
	}

	// Parse decimal quantities.
	qty, err := decimal.NewFromString(tc.QuantityDecimal)
	if err != nil || qty.IsZero() {
		c.sendAck(ctx, conn, wire.Ack{
			ClientOrderID: tc.ClientOrderID,
			Status:        wire.AckStatusRejected,
			ExchangeNowMs: clk,
			RejectReason:  "invalid quantity_decimal",
		})
		return
	}
	limit := decimal.Zero
	if tc.OrderType == "limit" {
		if tc.LimitPriceDecimal == "" {
			c.sendAck(ctx, conn, wire.Ack{
				ClientOrderID: tc.ClientOrderID,
				Status:        wire.AckStatusRejected,
				ExchangeNowMs: clk,
				RejectReason:  "limit order missing limit_price_decimal",
			})
			return
		}
		limit, err = decimal.NewFromString(tc.LimitPriceDecimal)
		if err != nil {
			c.sendAck(ctx, conn, wire.Ack{
				ClientOrderID: tc.ClientOrderID,
				Status:        wire.AckStatusRejected,
				ExchangeNowMs: clk,
				RejectReason:  "invalid limit_price_decimal",
			})
			return
		}
	}

	// Pre-record so a crash mid-submit doesn't double-execute on
	// retry: the next attempt will find an existing pending record
	// and report duplicate_pending.
	preRec := IdempotencyRecord{
		ClientOrderID: tc.ClientOrderID,
		Status:        IdempotencyStatusPending,
		SubmittedAtMs: clk,
		LastUpdatedMs: clk,
	}
	if err := c.idempotency.Put(preRec); err != nil {
		c.sendError(ctx, conn, wire.ErrorCodeInternalError, err.Error(), env.MsgID)
		return
	}

	// Submit to exchange.
	res, err := c.exchange.Submit(ctx, ExchangeOrder{
		ClientOrderID: tc.ClientOrderID,
		Symbol:        tc.Symbol,
		Side:          tc.Side,
		OrderType:     tc.OrderType,
		Quantity:      qty,
		LimitPrice:    limit,
		TimeInForce:   tc.TimeInForce,
	})
	if err != nil {
		_ = c.idempotency.UpdateStatus(tc.ClientOrderID, IdempotencyStatusRejected, "", c.nowMs())
		// errors.Unwrap returns nil for non-wrapped errors; use the
		// outer message directly to avoid a nil-pointer panic.
		c.sendAck(ctx, conn, wire.Ack{
			ClientOrderID: tc.ClientOrderID,
			Status:        wire.AckStatusRejected,
			ExchangeNowMs: c.nowMs(),
			RejectReason:  err.Error(),
		})
		return
	}

	// Determine the slippage reference: market → res.MarketRef from
	// exchange; limit → original LimitPrice.
	ref := res.MarketRef
	if tc.OrderType == "limit" {
		ref = limit
	}

	// Update idempotency to accepted, with exchange_order_id.
	rec := IdempotencyRecord{
		ClientOrderID:   tc.ClientOrderID,
		ExchangeOrderID: res.ExchangeOrderID,
		Status:          IdempotencyStatusAccepted,
		MarketRef:       ref,
		SubmittedAtMs:   preRec.SubmittedAtMs,
		LastUpdatedMs:   res.AcceptedAtMs,
	}

	// Send Ack first.
	c.sendAck(ctx, conn, wire.Ack{
		ClientOrderID:   tc.ClientOrderID,
		Status:          wire.AckStatusAccepted,
		ExchangeOrderID: res.ExchangeOrderID,
		ExchangeNowMs:   res.AcceptedAtMs,
	})

	// Mock fills immediately; surface OrderUpdate with fills. Bump
	// status to filled if the cumulative quantity matches the order.
	if len(res.Fills) > 0 {
		ou := wire.OrderUpdate{
			ClientOrderID:   tc.ClientOrderID,
			ExchangeOrderID: res.ExchangeOrderID,
			Status:          wire.OrderStatusFilled,
			Fills:           make([]wire.Fill, 0, len(res.Fills)),
		}
		cum := decimal.Zero
		for _, f := range res.Fills {
			cum = cum.Add(f.FillQuantity)
			slippageBps := computeSlippageBps(tc.Side, ref, f.FillPrice)
			wf := wire.Fill{
				FillQuantityDecimal:  formatDecimal(f.FillQuantity),
				FillPriceDecimal:     formatDecimal(f.FillPrice),
				FillFeeAsset:         f.FillFeeAsset,
				FillFeeAmountDecimal: formatDecimal(f.FillFeeAmount),
				FilledAtExchangeMs:   f.FilledAtExchangeMs,
				ActualSlippageBps:    slippageBps,
				TradeID:              f.TradeID,
			}
			ou.Fills = append(ou.Fills, wf)
			// Tee into the delta_report buffer (§5.11 fallback). Unlike
			// order_update.fills, delta_report fills must name their order
			// so SaaS can dedupe by (client_order_id, filled_at_exchange_ms).
			df := wf
			df.ClientOrderID = tc.ClientOrderID
			df.ExchangeOrderID = res.ExchangeOrderID
			c.delta.addFill(df)
		}
		ou.CumulativeFilledQuantityDecimal = formatDecimal(cum)

		// Set final status: filled if cum >= ordered qty; otherwise partial.
		if cum.GreaterThanOrEqual(qty) {
			ou.Status = wire.OrderStatusFilled
			rec.Status = IdempotencyStatusFilled
		} else {
			ou.Status = wire.OrderStatusPartialFilled
		}
		_ = c.sendTyped(ctx, conn, wire.TypeOrderUpdate, ou)
	}

	rec.LastUpdatedMs = c.nowMs()
	_ = c.idempotency.Put(rec)
}

// duplicateAck builds the Ack for a repeated client_order_id.
func (c *Client) duplicateAck(existing IdempotencyRecord, nowMs int64) wire.Ack {
	if existing.IsTerminal() {
		return wire.Ack{
			ClientOrderID:   existing.ClientOrderID,
			Status:          wire.AckStatusDuplicateTerminal,
			ExchangeOrderID: existing.ExchangeOrderID,
			ExchangeNowMs:   nowMs,
			RejectReason:    fmt.Sprintf("already %s", existing.Status),
		}
	}
	return wire.Ack{
		ClientOrderID:   existing.ClientOrderID,
		Status:          wire.AckStatusDuplicatePending,
		ExchangeOrderID: existing.ExchangeOrderID,
		ExchangeNowMs:   nowMs,
	}
}

// sendAck wraps sendTyped for the Ack case (most common write path).
func (c *Client) sendAck(ctx context.Context, conn wsconn.Conn, ack wire.Ack) {
	_ = c.sendTyped(ctx, conn, wire.TypeAck, ack)
}

// computeSlippageBps follows §8.2:
//
//   - market buy: (fill - ref) / ref × 10000   (positive = worse)
//   - market sell: (ref - fill) / ref × 10000
//   - limit (either side): same as market with limit_price as ref
//
// Sign convention: positive ⇒ worse than reference for the Agent's
// side. Returns float64 because bps is already a derived (lossy)
// quantity per §2.2.
func computeSlippageBps(side string, ref, fill decimal.Decimal) float64 {
	if ref.IsZero() {
		return 0
	}
	var diff decimal.Decimal
	switch side {
	case "buy":
		diff = fill.Sub(ref)
	case "sell":
		diff = ref.Sub(fill)
	default:
		return 0
	}
	bps := diff.Div(ref).Mul(decimal.NewFromInt(10000))
	f, _ := bps.Float64()
	return f
}
