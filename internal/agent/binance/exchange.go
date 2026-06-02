// exchange.go — Step 4 of the Binance adapter build. Wraps Client and
// satisfies agent.Exchange:
//
//	Submit     — limit orders are rejected (v1 scope); market orders
//	             delegate to SubmitMarket.
//	Positions  — delegates to Account.
//	Reachable  — backed by an atomic.Bool that a background ping loop
//	             updates every PingInterval (default 30s).
//
// Start(ctx) launches the ping goroutine; Close() (or cancelling the
// ctx passed to Start) stops it. Both are idempotent.
package binance

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"quantlab/internal/agent"
)

// Compile-time check that *Exchange implements agent.Exchange. If a
// future change to the interface drops a method off the impl, the
// build breaks here instead of at the cmd/agent wiring site.
var _ agent.Exchange = (*Exchange)(nil)

// Compile-time check that *Exchange satisfies the opt-in
// OrderEventStreamer capability (User Data Stream → async fills).
// If a refactor drops Subscribe, the Agent client's type assertion
// would silently stop wiring fills — this assertion makes that a
// build break instead.
var _ agent.OrderEventStreamer = (*Exchange)(nil)

// Default tunings for the Exchange struct. Picked so a healthy
// session never logs more than once per minute about Ping and so a
// dead Binance edge surfaces within ~1 ping interval to Reachable().
const (
	DefaultPingInterval     = 30 * time.Second
	defaultPingTimeout      = 5 * time.Second
	DefaultTimeSyncInterval = 5 * time.Minute
	defaultTimeSyncTimeout  = 5 * time.Second

	// timeSyncWarnThresholdMs is when |serverOffset| first becomes
	// noisy enough to flag. recvWindow defaults to 5s; a 1s drift is
	// non-fatal but worth a Warn so ops can spot a sick clock before
	// signed requests start being rejected.
	timeSyncWarnThresholdMs = 1000
)

// ExchangeOptions tunes the Binance adapter. Zero values produce
// production-ready defaults; tests override BaseURL + HTTPClient via
// httptest.Server.
type ExchangeOptions struct {
	// Inherited from Options on Client.
	BaseURL      string
	HTTPClient   *http.Client
	RecvWindowMs int
	NowFn        func() time.Time

	// PingInterval bounds Reachable() staleness. 0 → DefaultPingInterval.
	PingInterval time.Duration

	// TimeSyncInterval bounds how stale the cached server-clock offset
	// can get. 0 → DefaultTimeSyncInterval. Negative disables periodic
	// resync (first sync still runs at Start to surface configuration
	// errors before the first signed request).
	TimeSyncInterval time.Duration

	// UDS configuration. Zero values are sensible defaults.
	//   StreamBaseURL          — auto-derived from BaseURL when empty
	//                            (the WS API endpoint).
	//   UDSReconnectMin/Max    — backoff envelope on session failure.
	//   UDSDialer              — test injection seam; production uses
	//                            wsconn.Dial.
	//   UDSDisabled            — when true, Subscribe is still safe to
	//                            call but no background goroutine is
	//                            spawned. Use for ops scenarios where
	//                            the operator wants market-only
	//                            without UDS network traffic.
	StreamBaseURL   string
	UDSReconnectMin time.Duration
	UDSReconnectMax time.Duration
	UDSDialer       UDSDialer
	UDSDisabled     bool

	// Logger is used for ping success/failure structured logs.
	// nil → slog.Default().
	Logger *slog.Logger
}

// Exchange is the production-grade agent.Exchange backed by Binance
// Spot REST. One per Agent process is sufficient.
type Exchange struct {
	client           *Client
	pingInterval     time.Duration
	pingTimeout      time.Duration
	timeSyncInterval time.Duration
	timeSyncTimeout  time.Duration
	logger           *slog.Logger

	reachable atomic.Bool

	// uds is the User Data Stream runtime. nil when UDSDisabled. Even
	// when nil, Subscribe is safe (records the callback for the
	// hypothetical future Enable; ignored otherwise).
	uds          *uds
	pendingSubMu sync.Mutex
	pendingSubFn func(agent.OrderEvent)

	startOnce sync.Once
	closeOnce sync.Once
	started   atomic.Bool
	stopCh    chan struct{}
	// pingDoneCh / timeSyncDoneCh close when their respective goroutines
	// exit; Close() waits on both before returning.
	pingDoneCh     chan struct{}
	timeSyncDoneCh chan struct{}
}

// NewExchange constructs the adapter without starting the ping loop.
// Caller must invoke Start(ctx) to begin background reachability
// probing; Submit/Positions/Reachable are safe to call before Start
// (Reachable simply returns false until the first successful ping).
func NewExchange(apiKey, apiSecret string, opts ExchangeOptions) *Exchange {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	pi := opts.PingInterval
	if pi <= 0 {
		pi = DefaultPingInterval
	}
	// TimeSyncInterval semantics:
	//   0       → DefaultTimeSyncInterval (periodic resync on)
	//   < 0     → disable periodic resync (first sync still runs)
	//   > 0     → explicit interval
	tsi := opts.TimeSyncInterval
	if tsi == 0 {
		tsi = DefaultTimeSyncInterval
	}
	ex := &Exchange{
		client: NewClient(apiKey, apiSecret, Options{
			BaseURL:      opts.BaseURL,
			HTTPClient:   opts.HTTPClient,
			RecvWindowMs: opts.RecvWindowMs,
			NowFn:        opts.NowFn,
		}),
		pingInterval:     pi,
		pingTimeout:      defaultPingTimeout,
		timeSyncInterval: tsi,
		timeSyncTimeout:  defaultTimeSyncTimeout,
		logger:           logger,
		stopCh:           make(chan struct{}),
		pingDoneCh:       make(chan struct{}),
		timeSyncDoneCh:   make(chan struct{}),
	}
	if !opts.UDSDisabled {
		ex.uds = newUDS(ex.client, udsOptions{
			StreamBaseURL: opts.StreamBaseURL,
			ReconnectMin:  opts.UDSReconnectMin,
			ReconnectMax:  opts.UDSReconnectMax,
			Dialer:        opts.UDSDialer,
			Logger:        logger,
		})
	}
	return ex
}

// Client exposes the underlying REST client for ops / diagnostic use.
// Order placement goes through Submit; this is for endpoints not
// covered by the agent.Exchange interface.
func (e *Exchange) Client() *Client { return e.client }

// Start launches the background loops (ping + time sync + UDS).
// Subsequent calls are no-ops. The goroutines exit when ctx is
// cancelled OR Close is called, whichever happens first.
func (e *Exchange) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		e.started.Store(true)
		go e.pingLoop(ctx)
		go e.timeSyncLoop(ctx)
		if e.uds != nil {
			// Re-apply any Subscribe call that landed before Start so
			// the streamer doesn't drop events that arrive in the
			// first tick. (Subscribe is always safe pre-Start; this
			// is the bridge that makes that contract real.)
			e.pendingSubMu.Lock()
			if e.pendingSubFn != nil {
				e.uds.Subscribe(e.pendingSubFn)
			}
			e.pendingSubMu.Unlock()
			e.uds.Start(ctx)
		}
	})
}

// Close stops the background loops and waits for goroutines to exit.
// Idempotent. Safe to call before Start (returns immediately).
func (e *Exchange) Close() error {
	e.closeOnce.Do(func() {
		close(e.stopCh)
		if e.uds != nil {
			e.uds.Close()
		}
	})
	if e.started.Load() {
		<-e.pingDoneCh
		<-e.timeSyncDoneCh
		if e.uds != nil {
			e.uds.Wait()
		}
	}
	return nil
}

// Subscribe implements agent.OrderEventStreamer. If UDS is enabled the
// callback is forwarded to the streamer; otherwise the callback is
// retained so a later Start (with UDS enabled in a different build)
// would still apply it — but in the current binary, UDSDisabled means
// no events fire. v1 last-wins replacement semantics.
//
// Safe to call at any time, including before Start.
func (e *Exchange) Subscribe(cb func(agent.OrderEvent)) {
	if e.uds != nil {
		// Cache locally too — Start re-applies before launching the
		// streamer so the pre-Start window doesn't drop events.
		e.pendingSubMu.Lock()
		e.pendingSubFn = cb
		e.pendingSubMu.Unlock()
		e.uds.Subscribe(cb)
		return
	}
	e.pendingSubMu.Lock()
	e.pendingSubFn = cb
	e.pendingSubMu.Unlock()
}

// pingLoop runs the synchronous startup ping then a ticker loop. We
// run the first probe inline (with its own timeout) so a misconfigured
// API key is visible in Reachable() before the first Submit attempt.
func (e *Exchange) pingLoop(ctx context.Context) {
	defer close(e.pingDoneCh)

	e.doPing(ctx)

	t := time.NewTicker(e.pingInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-t.C:
			e.doPing(ctx)
		}
	}
}

// timeSyncLoop runs an inline first SyncTime then (unless disabled)
// resyncs every timeSyncInterval. The inline call surfaces a
// misconfigured base_url / firewall blocking unsigned endpoints before
// the first signed Submit, which would otherwise look like an
// API-credentials failure.
func (e *Exchange) timeSyncLoop(ctx context.Context) {
	defer close(e.timeSyncDoneCh)

	e.doTimeSync(ctx)

	if e.timeSyncInterval < 0 {
		return
	}
	t := time.NewTicker(e.timeSyncInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-e.stopCh:
			return
		case <-t.C:
			e.doTimeSync(ctx)
		}
	}
}

// doPing performs one Ping and updates reachable. Bounded by
// pingTimeout so a hung TCP socket cannot block the loop past one
// tick.
func (e *Exchange) doPing(ctx context.Context) {
	pingCtx, cancel := context.WithTimeout(ctx, e.pingTimeout)
	defer cancel()
	if err := e.client.Ping(pingCtx); err != nil {
		e.reachable.Store(false)
		e.logger.Warn("binance_ping_failed", "err", err)
		return
	}
	e.reachable.Store(true)
}

// doTimeSync performs one SyncTime call. Bounded by timeSyncTimeout
// so the loop cannot stall on a hung socket. Failures keep the prior
// offset (SyncTime is responsible for that invariant).
func (e *Exchange) doTimeSync(ctx context.Context) {
	syncCtx, cancel := context.WithTimeout(ctx, e.timeSyncTimeout)
	defer cancel()
	if err := e.client.SyncTime(syncCtx); err != nil {
		e.logger.Warn("binance_time_sync_failed",
			"err", err,
			"offset_ms", e.client.ServerOffsetMs())
		return
	}
	off := e.client.ServerOffsetMs()
	abs := off
	if abs < 0 {
		abs = -abs
	}
	if abs >= timeSyncWarnThresholdMs {
		e.logger.Warn("binance_time_sync_large_drift", "offset_ms", off)
	}
}

// ===== agent.Exchange =====

// Submit dispatches by order type. Both market and limit go through
// REST POST /api/v3/order via order.go; async fills for resting limit
// orders are surfaced by the User Data Stream wiring (Phase 7.11).
func (e *Exchange) Submit(ctx context.Context, order agent.ExchangeOrder) (*agent.ExchangeSubmitResult, error) {
	switch order.OrderType {
	case "market":
		return e.client.SubmitMarket(ctx, order)
	case "limit":
		return e.client.SubmitLimit(ctx, order)
	}
	return nil, fmt.Errorf("%w: unsupported order_type %q", agent.ErrExchangeRejected, order.OrderType)
}

// Positions snapshots the account's non-zero balances.
func (e *Exchange) Positions(ctx context.Context) ([]agent.Position, error) {
	return e.client.Account(ctx)
}

// Reachable reports whether the last Ping succeeded. Returns false
// before the first ping completes (typical window: 5-100ms after
// Start), regardless of whether Binance is actually up.
func (e *Exchange) Reachable() bool {
	return e.reachable.Load()
}
