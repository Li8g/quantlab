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

	// ExpectedEnvironment, if non-empty, is asserted against
	// Hello.Environment at handshake (backlog ⑥). RejectEnvMismatch makes
	// a mismatch a hard auth_fail (prod, app_role=saas); otherwise a
	// mismatch only warns and the connection proceeds (dev/lab testnet
	// workflow). Empty ExpectedEnvironment skips the assertion.
	ExpectedEnvironment string
	RejectEnvMismatch   bool

	// PriceCapBps is the B2 price-protection cap (decision-b2-limit-order-
	// price-protection.md). When > 0, the dispatcher rewrites each market
	// OrderIntent as a marketable LIMIT IOC priced at latestClose×(1±cap/1e4),
	// so a fill worse than the cap is rejected by the exchange instead of
	// executing at a flash price. 0 (the zero value) disables protection —
	// orders pass through as market, the pre-B2 behavior. Strategy-emitted
	// limit orders are never rewritten. The 50bps deployment default lives in
	// the saas config layer, not here, so wshub unit tests stay market-only.
	PriceCapBps float64

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

	// OnAgentMessage fires for every Agent-originated business message
	// (ack / order_update / delta_report) after the envelope has been
	// decoded. Step 4/5 will wire DB persistence (TradeRecord status
	// updates, SpotExecution inserts, discrepancy detection). v1 emits
	// a structured log line in addition to firing this hook.
	OnAgentMessage func(ctx context.Context, accountID string, env wire.Envelope) error

	// OnFrozenLookup reports whether accountID is currently kill-latched
	// (B1 server-side persistent latch). Consulted at handshake just before
	// auth_ok so the response can carry the durable frozen state, re-arming
	// an Agent that restarted or reconnected after an offline kill. Nil →
	// the latch is never re-asserted (pre-B1 behavior). A lookup error is
	// fail-closed: the connection treats the account as frozen, so a transient
	// store error can never silently un-kill a halted agent.
	OnFrozenLookup func(ctx context.Context, accountID string) (bool, error)

	// OnHandshakeReject fires when a connection is rejected during handshake
	// (e.g. env-mismatch auth_fail). accountID and code are always set; msg
	// is human-readable. Best-effort — errors are logged, not propagated.
	// The hook runs in the connection goroutine, so implementations must be
	// non-blocking (or use a background context).
	OnHandshakeReject func(ctx context.Context, accountID, code, msg string) error

	// OnConnectionState fires on Connection lifecycle transitions
	// (authed / ready / stale / disconnected) and on every Agent →
	// SaaS message (pong / ack / order_update / delta_report) to
	// refresh the Agent-online TTL per docs/saas-ws-protocol-v1.md §7.2.
	//
	// state is the current phase label; lastMsgID is the envelope
	// MsgID that drove the event (empty on pure transitions like
	// 'authed' or 'disconnected'). The hook is best-effort —
	// returned errors are logged but never tear the connection down.
	OnConnectionState func(ctx context.Context, ev ConnectionStateEvent) error
}

// ConnectionStateEvent is the payload delivered to OnConnectionState.
// State labels match docs/saas-ws-protocol-v1.md §7.2 connection_state
// enum.
type ConnectionStateEvent struct {
	AccountID string
	AgentID   string
	State     string // "connecting"|"authed"|"ready"|"stale"|"disconnected"
	LastMsgID string // optional — refresh signals carry the inbound MsgID
	NowMs     int64
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

	expectedEnv       string
	rejectEnvMismatch bool
	priceCapBps       float64

	onStateSync       func(ctx context.Context, accountID string, payload json.RawMessage) error
	onStale           func(ctx context.Context, accountID string) error
	onAgentMessage    func(ctx context.Context, accountID string, env wire.Envelope) error
	onConnectionState func(ctx context.Context, ev ConnectionStateEvent) error
	onHandshakeReject func(ctx context.Context, accountID, code, msg string) error
	onFrozenLookup    func(ctx context.Context, accountID string) (bool, error)
}

// New constructs a Hub with cfg overrides applied to the package defaults.
func New(auth *agentauth.Service, cfg Config) *Hub {
	h := &Hub{
		auth:              auth,
		registry:          NewRegistry(),
		log:               cfg.Logger,
		clock:             cfg.Clock,
		msgIDFn:           cfg.MsgIDFn,
		nowFn:             cfg.NowFn,
		pingInterval:      cfg.PingInterval,
		pongTimeout:       cfg.PongTimeout,
		pingMisses:        cfg.PingMisses,
		authTimeout:       cfg.AuthTimeout,
		stateSyncTimeout:  cfg.StateSyncTimeout,
		writeTimeout:      cfg.WriteTimeout,
		expectedEnv:       cfg.ExpectedEnvironment,
		rejectEnvMismatch: cfg.RejectEnvMismatch,
		priceCapBps:       cfg.PriceCapBps,
		onStateSync:       cfg.OnStateSync,
		onStale:           cfg.OnStale,
		onAgentMessage:    cfg.OnAgentMessage,
		onConnectionState: cfg.OnConnectionState,
		onHandshakeReject: cfg.OnHandshakeReject,
		onFrozenLookup:    cfg.OnFrozenLookup,
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
// symbol is the trading pair (instance-scoped) the Agent should target;
// OrderIntent carries no symbol since the strategy is bound to one
// pair via StrategyInput.
//
// Returns ErrAccountNotConnected if no Ready connection exists. Caller
// (instance.Manager.Tick) treats this as a non-fatal warning — the
// Agent will state-sync on next reconnect.
func (h *Hub) Dispatch(ctx context.Context, instanceID, accountID, symbol string, latestClose float64, orders []strategy.OrderIntent) error {
	if len(orders) == 0 {
		return nil
	}
	if latestClose <= 0 {
		return fmt.Errorf("wshub.Dispatch: invalid latestClose=%v", latestClose)
	}
	if symbol == "" {
		return errors.New("wshub.Dispatch: symbol empty")
	}
	cn, err := h.registry.Get(accountID)
	if err != nil {
		return err
	}
	if !cn.IsReady() {
		return ErrAccountNotConnected
	}
	for _, oi := range orders {
		tc, err := buildTradeCommand(oi, instanceID, symbol, latestClose, h.nowMs(), h.priceCapBps)
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

// SendKillSwitch delivers a kill_switch to one account's Agent (control
// plane of kill_switch Option 3). The Agent latches frozen and rejects
// subsequent trade_commands (see internal/agent handleKillSwitch).
//
// Returns ErrAccountNotConnected when no Ready connection exists. Unlike
// Dispatch, a miss here is significant: the operator/auto-trigger wanted
// to halt a specific account and couldn't reach it. The caller decides
// severity — a manual route surfaces it (e.g. 409); the auto-trigger
// logs + alerts out-of-band so a killed-but-offline Agent is noticed
// (it will replay the kill on reconnect once kill state is persisted —
// step 4).
func (h *Hub) SendKillSwitch(accountID string, ks wire.KillSwitch) error {
	cn, err := h.registry.Get(accountID)
	if err != nil {
		return err
	}
	if !cn.IsReady() {
		return ErrAccountNotConnected
	}
	raw, err := wire.EncodeMessage(wire.TypeKillSwitch, h.msgIDFn(),
		h.nowMs(), accountID, ks)
	if err != nil {
		return fmt.Errorf("wshub.SendKillSwitch: encode: %w", err)
	}
	if err := cn.SendFrame(raw); err != nil {
		return fmt.Errorf("wshub.SendKillSwitch: send: %w", err)
	}
	return nil
}

// buildTradeCommand turns one OrderIntent into a wire.TradeCommand by
// converting QuantityUSD (float64) → quantity_decimal (string) using
// latestClose. ClientOrderID falls through if non-empty; otherwise the
// caller is expected to assign one (Manager will).
//
// B2 price protection: when priceCapBps > 0 a *market* intent is rewritten
// as a marketable LIMIT IOC priced at latestClose×(1±cap/1e4) — the same
// latestClose used for the quantity conversion, so price and quantity are
// anchored to one decision-time reference (decision-b2-limit-order-price-
// protection.md §4.5). This is a dispatch-layer guardrail only: the strategy
// and backtest never see the cap, and because the backtest fills at
// close×(1±slippage) with cap ≥ slippage, the limit is numerically identical
// to the market fill it replaces (D3-a, no fitness_version event). A
// strategy-emitted limit order keeps its own price and GTC; only market
// intents are wrapped. cap == 0 passes market intents through unchanged.
//
// 8 decimal places matches Binance BTC step size; deeper precision would
// be silently truncated downstream. Symbol-specific step tables are
// Phase 8 polish.
func buildTradeCommand(oi strategy.OrderIntent, instanceID, symbol string, latestClose float64, nowMs int64, priceCapBps float64) (wire.TradeCommand, error) {
	if oi.ClientOrderID == "" {
		return wire.TradeCommand{}, errors.New("OrderIntent.ClientOrderID empty")
	}
	if oi.QuantityUSD <= 0 {
		return wire.TradeCommand{}, fmt.Errorf("OrderIntent.QuantityUSD=%v invalid", oi.QuantityUSD)
	}
	qty := oi.QuantityUSD / latestClose
	qtyStr := strconv.FormatFloat(qty, 'f', 8, 64)

	orderType := oi.OrderType
	limitPrice := oi.LimitPrice
	timeInForce := ""
	if oi.OrderType == strategy.OrderTypeMarket && priceCapBps > 0 {
		capPrice, err := capLimitPrice(oi.Side, latestClose, priceCapBps)
		if err != nil {
			return wire.TradeCommand{}, fmt.Errorf("OrderIntent %s: %w", oi.ClientOrderID, err)
		}
		orderType = strategy.OrderTypeLimit
		limitPrice = capPrice
		timeInForce = wire.TimeInForceIOC
	}

	tc := wire.TradeCommand{
		IntentKind:      wire.IntentKind(oi.Kind),
		ClientOrderID:   oi.ClientOrderID,
		InstanceID:      instanceID,
		Symbol:          symbol,
		Side:            string(oi.Side),
		OrderType:       string(orderType),
		QuantityDecimal: qtyStr,
		TimeInForce:     timeInForce,
		ValidUntilMs:    oi.ValidUntilMs,
		NowMsAtSaaS:     nowMs,
	}
	if orderType == strategy.OrderTypeLimit {
		tc.LimitPriceDecimal = strconv.FormatFloat(limitPrice, 'f', 8, 64)
	}
	return tc, nil
}

// capLimitPrice returns the marketable-limit price for a market intent under
// B2 price protection: buy → latestClose×(1+cap), sell → latestClose×(1−cap),
// so any fill is no worse than cap bps from latestClose. An unrecognized side
// is an error (the cap direction is undefined), not a silent passthrough.
func capLimitPrice(side strategy.OrderSide, latestClose, capBps float64) (float64, error) {
	switch side {
	case strategy.OrderSideBuy:
		return latestClose * (1 + capBps/1e4), nil
	case strategy.OrderSideSell:
		return latestClose * (1 - capBps/1e4), nil
	default:
		return 0, fmt.Errorf("invalid side %q for price cap", side)
	}
}
