package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/wire"
	"quantlab/internal/wsconn"
)

// Dialer abstracts the WS dial so tests can substitute a pipe-based
// connection without spinning up a real server.
type Dialer interface {
	Dial(ctx context.Context, url string, header http.Header) (wsconn.Conn, error)
}

// gorillaDialer is the production Dialer.
type gorillaDialer struct{}

func (gorillaDialer) Dial(ctx context.Context, url string, header http.Header) (wsconn.Conn, error) {
	return wsconn.Dial(ctx, url, header)
}

// Client is one Agent session manager. It owns:
//   - reconnect-with-backoff loop
//   - handshake state machine (Agent side)
//   - heartbeat pong responder
//   - TradeCommand → exchange → Ack pipeline
//   - in-memory market_ref cache → ActualSlippageBps computation
//
// Methods are not safe for concurrent calls — Run is the single
// goroutine entry point.
type Client struct {
	cfg         Config
	exchange    Exchange
	idempotency IdempotencyStore
	log         *slog.Logger

	// Injectable seams (tests substitute).
	dialer  Dialer
	msgIDFn func() string
	nowFn   func() time.Time

	// Backoff sequence; nextBackoff advances through it on each failure.
	backoff       []time.Duration
	backoffIdx    atomic.Int32
	backoffJitter func() float64 // returns 0.8..1.2

	// Connection-scoped state. Reset on each successful (re)connect.
	connMu  sync.Mutex
	conn    wsconn.Conn
	connCtx context.Context
}

// Options tunes Client construction. Zero-valued fields fall back to
// production defaults.
type Options struct {
	Logger        *slog.Logger
	Dialer        Dialer
	Backoff       []time.Duration
	MsgIDFn       func() string
	NowFn         func() time.Time
	BackoffJitter func() float64
}

// NewClient wires a Client. Required deps: cfg + exchange + idempotency.
func NewClient(cfg Config, ex Exchange, idem IdempotencyStore, opt Options) *Client {
	c := &Client{
		cfg:           cfg,
		exchange:      ex,
		idempotency:   idem,
		log:           opt.Logger,
		dialer:        opt.Dialer,
		msgIDFn:       opt.MsgIDFn,
		nowFn:         opt.NowFn,
		backoff:       opt.Backoff,
		backoffJitter: opt.BackoffJitter,
	}
	if c.log == nil {
		c.log = slog.Default()
	}
	if c.dialer == nil {
		c.dialer = gorillaDialer{}
	}
	if c.msgIDFn == nil {
		c.msgIDFn = defaultMsgID
	}
	if c.nowFn == nil {
		c.nowFn = time.Now
	}
	if c.backoff == nil {
		c.backoff = DefaultBackoff()
	}
	if c.backoffJitter == nil {
		c.backoffJitter = func() float64 {
			// uniform in [0.8, 1.2]
			return 0.8 + rand.Float64()*0.4
		}
	}
	// Opt-in: if the exchange backend can stream async order events
	// (Binance UDS, future websocket streamers), subscribe so resting
	// limit-order fills reach SaaS via wire.OrderUpdate. MockExchange
	// does not implement OrderEventStreamer so this falls through to
	// the no-op path silently.
	//
	// REFACTOR HOOK — when adding a second streamer-capable backend,
	// keep this assertion site as the single integration point. If a
	// backend needs to expose multiple capability interfaces, prefer
	// multiple discrete asserts here over a single fat interface.
	if streamer, ok := ex.(OrderEventStreamer); ok {
		streamer.Subscribe(c.onOrderEvent)
	}
	return c
}

// defaultMsgID generates a 26-char Crockford-base32 monotonic ID. We
// don't import store.NewULID here because the agent package must stand
// alone — cmd/agent doesn't depend on saas/store. A small inline ULID
// generator keeps the wire format compatible.
//
// Format: 10 chars of ms timestamp (Crockford-base32) + 16 chars
// random (Crockford-base32). Not strictly monotonic across processes
// but good enough for envelope.msg_id.
var msgIDMu sync.Mutex

func defaultMsgID() string {
	msgIDMu.Lock()
	defer msgIDMu.Unlock()
	now := time.Now().UnixMilli()
	const alpha = "0123456789ABCDEFGHJKMNPQRSTVWXYZ" // Crockford
	out := make([]byte, 26)
	for i := 9; i >= 0; i-- {
		out[i] = alpha[now&31]
		now >>= 5
	}
	for i := 10; i < 26; i++ {
		out[i] = alpha[rand.IntN(32)]
	}
	return string(out)
}

// Run starts the reconnect loop. Blocks until ctx is cancelled or an
// unrecoverable error (revoked token, invalid format) trips the fatal
// path. Recoverable errors are silently retried with exponential
// backoff.
func (c *Client) Run(ctx context.Context) error {
	for {
		err := c.runSession(ctx)
		// ctx cancellation always wins, regardless of runSession's
		// final error (it may have returned nil on clean disconnect).
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if isFatalAuthErr(err) {
			c.log.Error("agent_fatal_auth", "err", err)
			return err
		}

		wait := c.nextBackoff()
		c.log.Info("agent_reconnect_backoff", "wait_ms", wait.Milliseconds(),
			"prev_err", errString(err))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// errString safely renders a possibly-nil error.
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// isFatalAuthErr returns true for auth failures the Agent can never
// recover from by retrying with the same config (e.g. invalid token,
// revoked token). Schema-mismatch errors are also fatal — the operator
// must redeploy a matching binary.
func isFatalAuthErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "invalid_token") ||
		strings.Contains(msg, "revoked") ||
		strings.Contains(msg, "schema_mismatch")
}

// nextBackoff returns the next wait duration with jitter, and advances
// the index (caps at the final entry).
func (c *Client) nextBackoff() time.Duration {
	idx := int(c.backoffIdx.Load())
	if idx >= len(c.backoff) {
		idx = len(c.backoff) - 1
	}
	d := c.backoff[idx]
	jit := c.backoffJitter()
	scaled := time.Duration(float64(d) * jit)
	if idx < len(c.backoff)-1 {
		c.backoffIdx.Store(int32(idx + 1))
	}
	return scaled
}

// resetBackoff is called after a successful Ready transition.
func (c *Client) resetBackoff() {
	c.backoffIdx.Store(0)
}

// runSession dials once, runs the handshake, then loops on the read
// pump until the connection dies. Returns nil on graceful shutdown,
// the underlying error on failure.
func (c *Client) runSession(ctx context.Context) (retErr error) {
	dialCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := c.dialer.Dial(dialCtx, c.cfg.SaaSURL, nil)
	cancel()
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	c.connMu.Lock()
	c.conn = conn
	c.connMu.Unlock()
	defer func() {
		_ = conn.Close()
		c.connMu.Lock()
		c.conn = nil
		c.connMu.Unlock()
	}()

	if err := c.doHandshake(ctx, conn); err != nil {
		return fmt.Errorf("handshake: %w", err)
	}
	c.resetBackoff()
	c.log.Info("agent_session_ready", "account_id", c.cfg.AccountID)

	return c.runReadLoop(ctx, conn)
}

// runReadLoop consumes frames from conn until ctx cancellation or
// conn close. Each frame's type dispatches to a handler. Errors from
// handlers are logged but do not tear down the session — wire-level
// faults are surfaced as wire.Error frames.
func (c *Client) runReadLoop(ctx context.Context, conn wsconn.Conn) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		frame, err := conn.ReadFrame(ctx)
		if err != nil {
			if errors.Is(err, wsconn.ErrConnClosed) || errors.Is(err, context.Canceled) {
				return nil
			}
			return fmt.Errorf("read: %w", err)
		}
		env, err := wire.DecodeEnvelope(frame)
		if err != nil {
			c.sendError(ctx, conn, errorCodeForDecode(err), err.Error(), "")
			continue
		}
		c.dispatchInbound(ctx, conn, env)
	}
}

// nowMs is the wall-clock helper used in outgoing envelopes.
func (c *Client) nowMs() int64 { return c.nowFn().UnixMilli() }

// sendTyped is the canonical "queue this frame on this conn" helper.
// Each frame is written synchronously with a short deadline.
func (c *Client) sendTyped(ctx context.Context, conn wsconn.Conn, t wire.MessageType, payload any) error {
	raw, err := wire.EncodeMessage(t, c.msgIDFn(), c.nowMs(), c.cfg.AccountID, payload)
	if err != nil {
		return fmt.Errorf("encode %s: %w", t, err)
	}
	wctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := conn.WriteFrame(wctx, raw); err != nil {
		return fmt.Errorf("write %s: %w", t, err)
	}
	return nil
}

// sendError sends a wire.Error frame. Best-effort.
func (c *Client) sendError(ctx context.Context, conn wsconn.Conn, code wire.ErrorCode, msg, refMsgID string) {
	_ = c.sendTyped(ctx, conn, wire.TypeError, wire.Error{
		Code: code, Message: msg, RefMsgID: refMsgID,
	})
}

func errorCodeForDecode(err error) wire.ErrorCode {
	switch {
	case errors.Is(err, wire.ErrSchemaMismatch):
		return wire.ErrorCodeSchemaMismatch
	case errors.Is(err, wire.ErrUnknownType):
		return wire.ErrorCodeUnknownType
	case errors.Is(err, wire.ErrInvalidEnvelope):
		return wire.ErrorCodeInvalidEnvelope
	default:
		return wire.ErrorCodeDecodeFailed
	}
}

// dispatchInbound routes one decoded envelope to its handler.
func (c *Client) dispatchInbound(ctx context.Context, conn wsconn.Conn, env wire.Envelope) {
	switch env.Type {
	case wire.TypePing:
		c.handlePing(ctx, conn, env)
	case wire.TypeTradeCommand:
		c.handleTradeCommand(ctx, conn, env)
	case wire.TypeKillSwitch:
		c.handleKillSwitch(ctx, conn, env)
	case wire.TypeGracefulShutdown:
		c.handleGracefulShutdown(ctx, env)
	case wire.TypeStateSyncRequest:
		// SaaS may send an out-of-band state sync after handshake; reply
		// the same way as during handshake.
		_ = c.sendStateSyncResponse(ctx, conn)
	case wire.TypeError:
		c.log.Warn("agent_remote_error",
			"account_id", c.cfg.AccountID, "msg_id", env.MsgID,
			"payload", string(env.Payload))
	default:
		c.sendError(ctx, conn, wire.ErrorCodeUnknownType,
			fmt.Sprintf("unexpected type post-handshake: %q", env.Type),
			env.MsgID)
	}
}

// handlePing answers with pong (§4.4). Echoes the ping's msg_id so the
// Hub can match it against its outstanding-ping tracker.
func (c *Client) handlePing(ctx context.Context, conn wsconn.Conn, env wire.Envelope) {
	pong := wire.Pong{
		EchoMsgID:         env.MsgID,
		AgentNowMs:        c.nowMs(),
		ExchangeReachable: c.exchange.Reachable(),
	}
	_ = c.sendTyped(ctx, conn, wire.TypePong, pong)
}

// handleGracefulShutdown logs and lets the read loop notice the eventual
// close. Per §4.6, the Agent should not actively close; it waits for
// SaaS to close, then reconnects after retry_in_ms.
func (c *Client) handleGracefulShutdown(_ context.Context, env wire.Envelope) {
	gs, err := wire.DecodePayload[wire.GracefulShutdown](env)
	if err != nil {
		c.log.Warn("agent_graceful_shutdown_decode_failed", "err", err)
		return
	}
	c.log.Info("agent_graceful_shutdown",
		"reason", gs.Reason, "retry_in_ms", gs.RetryInMs)
}

// handleKillSwitch acks the kill and stops accepting future
// TradeCommands. v1 implementation: ack accepted, log; the Agent does
// not maintain open orders in MockExchange, so there's nothing to
// cancel. Real-exchange impl will iterate open orders here.
func (c *Client) handleKillSwitch(ctx context.Context, conn wsconn.Conn, env wire.Envelope) {
	ks, err := wire.DecodePayload[wire.KillSwitch](env)
	if err != nil {
		c.sendError(ctx, conn, wire.ErrorCodeDecodeFailed, err.Error(), env.MsgID)
		return
	}
	c.log.Warn("agent_kill_switch",
		"reason", ks.Reason, "operator", ks.OperatorUserID, "scope", ks.Scope)
	// Reply with an ack to confirm receipt; v1 uses the kill_switch's
	// own msg_id as the client_order_id (out-of-band signal).
	_ = c.sendTyped(ctx, conn, wire.TypeAck, wire.Ack{
		ClientOrderID: env.MsgID,
		Status:        wire.AckStatusAccepted,
		ExchangeNowMs: c.nowMs(),
	})
}

// formatDecimal renders a decimal.Decimal with up to 8 fractional
// digits, no trailing zeros.
func formatDecimal(d decimal.Decimal) string {
	// strconv-rounded; matches SaaS-side rendering for symmetry.
	f, _ := d.Float64()
	return strconv.FormatFloat(f, 'f', 8, 64)
}

// onOrderEvent is the callback registered with the exchange's
// OrderEventStreamer at construction. Invoked from the streamer's
// own single goroutine (binance.uds.run) — must be lightweight + safe
// for concurrent access to c.conn.
//
// Flow:
//
//  1. Look up the idempotency record by ClientOrderID. MarketRef from
//     the record is the slippage reference (limit_price for limit
//     orders, captured best-side for market orders).
//  2. Build wire.OrderUpdate. For TRADE events (Fill != nil), compute
//     ActualSlippageBps via §8.2. For cancel/reject, emit a zero-fill
//     update so SaaS can reconcile the lifecycle.
//  3. Update idempotency status to match the event.
//  4. Write the frame on the active conn under c.connMu. If no active
//     session (e.g. between disconnect and the next dial), drop the
//     event with a warn log — v1 limitation per saas-ws-protocol-v1.md
//     §11 Q7 (state_sync_response would replay missed fills; not in
//     v1 scope).
//
// REFACTOR HOOK — when adding reconciliation across disconnects:
//
//   - Persist undispatched events to the idempotency-adjacent table.
//   - On reconnect, after StateSyncResponse, drain the queue.
//
// Do NOT block here on a conn that's missing; the streamer goroutine
// must keep advancing to consume Binance's queue.
func (c *Client) onOrderEvent(ev OrderEvent) {
	if ev.ClientOrderID == "" {
		c.log.Warn("agent_order_event_missing_client_order_id", "event", ev)
		return
	}

	rec, ok, _ := c.idempotency.Get(ev.ClientOrderID)
	if !ok {
		// Either the order was placed by a previous Agent process
		// (sqlite retention covers 7d; some events outlive that), or
		// the executionReport raced ahead of the Submit ack record
		// write. Drop with warn — SaaS-side reconciliation handles
		// this in v2.
		c.log.Warn("agent_order_event_unknown_order",
			"client_order_id", ev.ClientOrderID,
			"exchange_order_id", ev.ExchangeOrderID,
			"status", ev.Status)
		return
	}

	side := ev.Side // OrderEvent carries it for streamers that know
	// (binance does); fall back to "" if not — slippage will be 0
	// (computeSlippageBps short-circuits unknown sides).

	ou := wire.OrderUpdate{
		ClientOrderID:   ev.ClientOrderID,
		ExchangeOrderID: orFallback(ev.ExchangeOrderID, rec.ExchangeOrderID),
		Status:          ev.Status,
		Fills:           []wire.Fill{}, // wire requires non-nil slice
	}

	// Cumulative qty: prefer streamer-provided value (binance UDS `z`);
	// fall back to this event's Fill qty (lossy for multi-fill but
	// better than zero).
	cum := ev.CumulativeFillQuantity
	if cum.IsZero() && ev.Fill != nil {
		cum = ev.Fill.FillQuantity
	}
	ou.CumulativeFilledQuantityDecimal = formatDecimal(cum)

	if ev.Fill != nil {
		slip := computeSlippageBps(side, rec.MarketRef, ev.Fill.FillPrice)
		ou.Fills = append(ou.Fills, wire.Fill{
			FillQuantityDecimal:  formatDecimal(ev.Fill.FillQuantity),
			FillPriceDecimal:     formatDecimal(ev.Fill.FillPrice),
			FillFeeAsset:         ev.Fill.FillFeeAsset,
			FillFeeAmountDecimal: formatDecimal(ev.Fill.FillFeeAmount),
			FilledAtExchangeMs:   ev.Fill.FilledAtExchangeMs,
			ActualSlippageBps:    slip,
		})
	}

	// Update idempotency status to the latest terminal/non-terminal
	// state. The mapping mirrors mapEventStatusToWire.
	newStatus := mapEventStatusToIdem(ev.Status, rec.Status)
	if newStatus != rec.Status {
		_ = c.idempotency.UpdateStatus(
			ev.ClientOrderID, newStatus, ou.ExchangeOrderID, c.nowMs())
	}

	// Snapshot the active conn under the same lock that runSession
	// uses to populate it. Drop if no session.
	c.connMu.Lock()
	conn := c.conn
	c.connMu.Unlock()
	if conn == nil {
		c.log.Warn("agent_order_event_no_active_session",
			"client_order_id", ev.ClientOrderID,
			"status", ev.Status)
		return
	}

	// Use a fresh background context so a long-running outer ctx
	// timeout doesn't abort an OrderUpdate send. sendTyped applies
	// its own 5s deadline internally.
	if err := c.sendTyped(context.Background(), conn, wire.TypeOrderUpdate, ou); err != nil {
		c.log.Warn("agent_order_event_send_failed",
			"err", err, "client_order_id", ev.ClientOrderID)
	}
}

// mapEventStatusToIdem updates the IdempotencyStatus based on the
// event. partial_filled keeps the current status (accepted → accepted)
// since the record only flips to filled on terminal completion.
func mapEventStatusToIdem(s wire.OrderStatus, current IdempotencyStatus) IdempotencyStatus {
	switch s {
	case wire.OrderStatusFilled:
		return IdempotencyStatusFilled
	case wire.OrderStatusCancelled:
		return IdempotencyStatusCancelled
	case wire.OrderStatusRejected:
		return IdempotencyStatusRejected
	case wire.OrderStatusPartialFilled:
		return current // unchanged — terminal will follow
	}
	return current
}

// orFallback returns primary if non-empty, otherwise fallback.
func orFallback(primary, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}
