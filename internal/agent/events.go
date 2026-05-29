package agent

import (
	"github.com/shopspring/decimal"

	"quantlab/internal/wire"
)

// OrderEvent is one async order-state transition surfaced by an
// exchange-driven event stream (e.g. Binance User Data Stream). The
// Agent translates these into wire.OrderUpdate frames for SaaS.
//
// Status is the wire-level enum directly (see
// docs/saas-ws-protocol-v1.md §5.10):
//
//   - OrderStatusPartialFilled — a new Fill is included; cumulative
//     below ordered quantity.
//   - OrderStatusFilled        — terminal; cumulative >= ordered.
//   - OrderStatusCancelled     — order removed without (further) fills.
//   - OrderStatusRejected      — exchange rejected post-acceptance
//     (rare; most surface synchronously).
//
// Fill is non-nil only when Status is partial_filled or filled AND
// a new execution is carried by this event. A NEW→CANCELED transition
// has Fill=nil.
//
// ExchangeOrderID is required so the SaaS side can reconcile if the
// Agent restarted between Submit and the async event (idempotency
// store carries the mapping client_order_id → exchange_order_id).
//
// Side is the lowercase "buy" / "sell" originally submitted. Carried
// on every event so the slippage-bps computation (§8.2, sign flips
// for sell) doesn't have to round-trip the idempotency store. Empty
// is allowed when the source doesn't know — Agent falls back to the
// idempotency record's submission context, dropping the event with
// a warn log if neither has it.
type OrderEvent struct {
	ClientOrderID   string
	ExchangeOrderID string
	Status          wire.OrderStatus
	Side            string        // "buy" | "sell"; lowercased
	Fill            *ExchangeFill // optional; one execution carried by this event

	// CumulativeFillQuantity is the running total quantity filled for
	// this order across all events (Binance UDS field `z`). Used to
	// populate wire.OrderUpdate.CumulativeFilledQuantityDecimal.
	// Zero when the source doesn't report it; the Agent then falls
	// back to Fill.FillQuantity (lossy for multi-fill, which is OK in
	// v1 — accuracy improves when streamers populate it).
	CumulativeFillQuantity decimal.Decimal
}

// OrderEventStreamer is the opt-in capability interface for Exchange
// backends that push async order-state changes (post-acceptance
// fills on resting limit orders, manual cancellations from the
// exchange UI, etc.) to the Agent.
//
// Agent.Client probes for this capability at startup via a type
// assertion; exchange backends that don't implement it (e.g.
// MockExchange — fills inline within Submit) are silently skipped.
// Compile-time proof that a specific backend supports the interface
// is wired in the implementing package:
//
//	var _ agent.OrderEventStreamer = (*binance.Exchange)(nil)
//
// REFACTOR HOOK — when adding capabilities like Cancel(orderID) or
// ReplaceOrder() that some backends support and others don't, prefer
// a new dedicated interface (OrderCanceler, OrderReplacer, ...) over
// hanging methods off agent.Exchange. The Exchange interface is the
// synchronous IO contract; capabilities that imply background
// goroutines or async callbacks belong in their own interfaces so
// mocks and partial backends don't carry no-op samples (ISP).
//
// REFACTOR HOOK — if v2 needs multiple subscribers (e.g. a metrics
// hook alongside the wire dispatch), evolve Subscribe to either
// return an unsubscribe handle or switch to a slice of callbacks
// fanned out under a mutex. v1 keeps it last-wins replacement to
// avoid premature complexity — the single caller is Client.Run.
type OrderEventStreamer interface {
	// Subscribe registers a callback invoked from the streamer's own
	// single goroutine. The callback must return quickly; heavy work
	// should be offloaded by the callback itself.
	//
	// At most one active subscription per streamer; subsequent calls
	// replace the previous callback rather than multiplexing.
	// Subscribe is safe to call before or after the stream goroutine
	// has been started — the callback simply applies to the next
	// event the goroutine processes.
	Subscribe(cb func(OrderEvent))
}
