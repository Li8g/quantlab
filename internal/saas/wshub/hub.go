package wshub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"quantlab/internal/saas/agentauth"
	"quantlab/internal/saas/store"
	"quantlab/internal/strategy"
	"quantlab/internal/wire"
	"quantlab/internal/wsconn"
)

// Defaults from docs/saas-ws-protocol-v1.md §4.4 + §4.2.
const (
	DefaultPingInterval     = 30 * time.Second
	DefaultPongTimeout      = 5 * time.Second
	DefaultPingMisses       = 3
	DefaultAuthTimeout      = 10 * time.Second
	DefaultStateSyncTimeout = 1500 * time.Millisecond
	DefaultWriteTimeout     = 10 * time.Second
)

// Config tunes the Hub. Zero values fall back to the docs/saas-ws-protocol-v1.md
// defaults; tests override via wshub.New(... Config{...}).
type Config struct {
	PingInterval     time.Duration
	PongTimeout      time.Duration
	PingMisses       int
	AuthTimeout      time.Duration
	StateSyncTimeout time.Duration
	WriteTimeout     time.Duration

	// Logger; nil → slog.Default().
	Logger *slog.Logger

	// Clock; nil → realClock.
	Clock Clock

	// MsgIDFn / NowFn; nil → store.NewULID / time.Now. Tests inject
	// deterministic versions.
	MsgIDFn func() string
	NowFn   func() time.Time

	// OnStateSync runs when an Agent supplies state_sync_response. The
	// raw payload is the JSON object (positions + open_orders + fills).
	// Hub does not interpret — Step 4/5 wires the reconciliation logic.
	OnStateSync func(ctx context.Context, accountID string, payload json.RawMessage) error

	// OnStale runs when heartbeat misses cross the threshold. Step 4/5
	// will wire an AuditLog entry + Redis online-state update.
	OnStale func(ctx context.Context, accountID string) error
}

// Hub is the per-process Agent WS server. One per cmd/saas process.
type Hub struct {
	auth     *agentauth.Service
	registry *Registry
	log      *slog.Logger
	clock    Clock

	msgIDFn func() string
	nowFn   func() time.Time

	pingInterval     time.Duration
	pongTimeout      time.Duration
	pingMisses       int
	authTimeout      time.Duration
	stateSyncTimeout time.Duration
	writeTimeout     time.Duration

	onStateSync func(ctx context.Context, accountID string, payload json.RawMessage) error
	onStale     func(ctx context.Context, accountID string) error
}

// New constructs a Hub with cfg overrides applied to the package defaults.
func New(auth *agentauth.Service, cfg Config) *Hub {
	h := &Hub{
		auth:             auth,
		registry:         NewRegistry(),
		log:              cfg.Logger,
		clock:            cfg.Clock,
		msgIDFn:          cfg.MsgIDFn,
		nowFn:            cfg.NowFn,
		pingInterval:     cfg.PingInterval,
		pongTimeout:      cfg.PongTimeout,
		pingMisses:       cfg.PingMisses,
		authTimeout:      cfg.AuthTimeout,
		stateSyncTimeout: cfg.StateSyncTimeout,
		writeTimeout:     cfg.WriteTimeout,
		onStateSync:      cfg.OnStateSync,
		onStale:          cfg.OnStale,
	}
	if h.log == nil {
		h.log = slog.Default()
	}
	if h.clock == nil {
		h.clock = realClock{}
	}
	if h.msgIDFn == nil {
		h.msgIDFn = store.NewULID
	}
	if h.nowFn == nil {
		h.nowFn = time.Now
	}
	if h.pingInterval == 0 {
		h.pingInterval = DefaultPingInterval
	}
	if h.pongTimeout == 0 {
		h.pongTimeout = DefaultPongTimeout
	}
	if h.pingMisses == 0 {
		h.pingMisses = DefaultPingMisses
	}
	if h.authTimeout == 0 {
		h.authTimeout = DefaultAuthTimeout
	}
	if h.stateSyncTimeout == 0 {
		h.stateSyncTimeout = DefaultStateSyncTimeout
	}
	if h.writeTimeout == 0 {
		h.writeTimeout = DefaultWriteTimeout
	}
	return h
}

// Registry exposes the connection registry for ops/admin endpoints.
func (h *Hub) Registry() *Registry { return h.registry }

// now/nowMs are internal accessors used by Connection.
func (h *Hub) now() time.Time { return h.nowFn() }
func (h *Hub) nowMs() int64   { return h.nowFn().UnixMilli() }

// ServeWS is the http.HandlerFunc that upgrades incoming connections and
// hands them off to Connection.Run. Blocks the request goroutine until
// the connection closes — gin compatible (a *gin.Context calls
// HandlerFunc(c.Writer, c.Request)).
func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	c, err := wsconn.ServerUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrader has already written the HTTP error.
		h.log.Warn("ws_upgrade_failed", "err", err, "remote", r.RemoteAddr)
		return
	}
	conn := wsconn.NewGorillaConn(c)
	h.runConn(r.Context(), conn)
}

// runConn is the test-friendly seam: ServeWS upgrades the socket, then
// calls runConn. Tests can call runConn directly with a pipeConn.
// Waits for writePump to drain before returning, so callers can rely on
// "runConn returned ⇒ all buffered frames flushed".
func (h *Hub) runConn(ctx context.Context, conn Conn) {
	cn := NewConnection(h, conn, h.log)
	defer h.registry.Unregister(cn)
	cn.Run(ctx)
	cn.Wait()
}

// Dispatch implements instance.TradeCommandDispatcher. It looks up the
// Agent for accountID, converts each OrderIntent into a wire.TradeCommand
// (using latestClose to render quantity_decimal), and enqueues the
// frames on the connection's outbox.
//
// Returns ErrAccountNotConnected if no Ready connection exists. Caller
// (instance.Manager.Tick) treats this as a non-fatal warning — the
// Agent will state-sync on next reconnect.
func (h *Hub) Dispatch(ctx context.Context, instanceID, accountID string, latestClose float64, orders []strategy.OrderIntent) error {
	if len(orders) == 0 {
		return nil
	}
	if latestClose <= 0 {
		return fmt.Errorf("wshub.Dispatch: invalid latestClose=%v", latestClose)
	}
	cn, err := h.registry.Get(accountID)
	if err != nil {
		return err
	}
	if !cn.IsReady() {
		return ErrAccountNotConnected
	}
	for _, oi := range orders {
		tc, err := buildTradeCommand(oi, instanceID, latestClose, h.nowMs())
		if err != nil {
			return fmt.Errorf("wshub.Dispatch: build %s: %w", oi.ClientOrderID, err)
		}
		raw, err := wire.EncodeMessage(wire.TypeTradeCommand, h.msgIDFn(),
			h.nowMs(), accountID, tc)
		if err != nil {
			return fmt.Errorf("wshub.Dispatch: encode: %w", err)
		}
		if err := cn.SendFrame(raw); err != nil {
			return fmt.Errorf("wshub.Dispatch: send: %w", err)
		}
	}
	_ = ctx
	return nil
}

// BroadcastGracefulShutdown sends graceful_shutdown to every Ready
// connection. Used by cmd/saas/main.go during SIGTERM handling.
func (h *Hub) BroadcastGracefulShutdown(retryIn time.Duration) {
	payload := wire.GracefulShutdown{
		Reason:    wire.GracefulShutdownSaaSRestart,
		RetryInMs: retryIn.Milliseconds(),
	}
	for _, cn := range h.registry.Snapshot() {
		if !cn.IsReady() {
			continue
		}
		raw, err := wire.EncodeMessage(wire.TypeGracefulShutdown, h.msgIDFn(),
			h.nowMs(), cn.AccountID, payload)
		if err != nil {
			h.log.Warn("ws_broadcast_encode_failed", "err", err)
			continue
		}
		_ = cn.SendFrame(raw)
	}
}

// buildTradeCommand turns one OrderIntent into a wire.TradeCommand by
// converting QuantityUSD (float64) → quantity_decimal (string) using
// latestClose. ClientOrderID falls through if non-empty; otherwise the
// caller is expected to assign one (Manager will).
//
// 8 decimal places matches Binance BTC step size; deeper precision would
// be silently truncated downstream. Symbol-specific step tables are
// Phase 8 polish.
func buildTradeCommand(oi strategy.OrderIntent, instanceID string, latestClose float64, nowMs int64) (wire.TradeCommand, error) {
	if oi.ClientOrderID == "" {
		return wire.TradeCommand{}, errors.New("OrderIntent.ClientOrderID empty")
	}
	if oi.QuantityUSD <= 0 {
		return wire.TradeCommand{}, fmt.Errorf("OrderIntent.QuantityUSD=%v invalid", oi.QuantityUSD)
	}
	qty := oi.QuantityUSD / latestClose
	qtyStr := strconv.FormatFloat(qty, 'f', 8, 64)
	tc := wire.TradeCommand{
		IntentKind:      wire.IntentKind(oi.Kind),
		ClientOrderID:   oi.ClientOrderID,
		InstanceID:      instanceID,
		Symbol:          "",     // TODO Step 4/5: lookup from instance metadata
		Side:            string(oi.Side),
		OrderType:       string(oi.OrderType),
		QuantityDecimal: qtyStr,
		ValidUntilMs:    oi.ValidUntilMs,
		NowMsAtSaaS:     nowMs,
	}
	if oi.OrderType == strategy.OrderTypeLimit {
		tc.LimitPriceDecimal = strconv.FormatFloat(oi.LimitPrice, 'f', 8, 64)
	}
	return tc, nil
}
