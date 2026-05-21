// exchange.go — Step 4 of the Binance adapter build. Wraps Client and
// satisfies agent.Exchange:
//
//   Submit     — limit orders are rejected (v1 scope); market orders
//                delegate to SubmitMarket.
//   Positions  — delegates to Account.
//   Reachable  — backed by an atomic.Bool that a background ping loop
//                updates every PingInterval (default 30s).
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

// Default tunings for the Exchange struct. Picked so a healthy
// session never logs more than once per minute about Ping and so a
// dead Binance edge surfaces within ~1 ping interval to Reachable().
const (
	DefaultPingInterval = 30 * time.Second
	defaultPingTimeout  = 5 * time.Second
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

	// Logger is used for ping success/failure structured logs.
	// nil → slog.Default().
	Logger *slog.Logger
}

// Exchange is the production-grade agent.Exchange backed by Binance
// Spot REST. One per Agent process is sufficient.
type Exchange struct {
	client       *Client
	pingInterval time.Duration
	pingTimeout  time.Duration
	logger       *slog.Logger

	reachable atomic.Bool

	startOnce sync.Once
	closeOnce sync.Once
	started   atomic.Bool
	stopCh    chan struct{}
	doneCh    chan struct{}
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
	return &Exchange{
		client: NewClient(apiKey, apiSecret, Options{
			BaseURL:      opts.BaseURL,
			HTTPClient:   opts.HTTPClient,
			RecvWindowMs: opts.RecvWindowMs,
			NowFn:        opts.NowFn,
		}),
		pingInterval: pi,
		pingTimeout:  defaultPingTimeout,
		logger:       logger,
		stopCh:       make(chan struct{}),
		doneCh:       make(chan struct{}),
	}
}

// Client exposes the underlying REST client for ops / diagnostic use.
// Order placement goes through Submit; this is for endpoints not
// covered by the agent.Exchange interface.
func (e *Exchange) Client() *Client { return e.client }

// Start launches the ping loop. Subsequent calls are no-ops. The
// goroutine exits when ctx is cancelled OR Close is called, whichever
// happens first.
func (e *Exchange) Start(ctx context.Context) {
	e.startOnce.Do(func() {
		e.started.Store(true)
		go e.pingLoop(ctx)
	})
}

// Close stops the ping loop and waits for the goroutine to exit.
// Idempotent. Safe to call before Start (returns immediately).
func (e *Exchange) Close() error {
	e.closeOnce.Do(func() {
		close(e.stopCh)
	})
	if e.started.Load() {
		<-e.doneCh
	}
	return nil
}

// pingLoop runs the synchronous startup ping then a ticker loop. We
// run the first probe inline (with its own timeout) so a misconfigured
// API key is visible in Reachable() before the first Submit attempt.
func (e *Exchange) pingLoop(ctx context.Context) {
	defer close(e.doneCh)

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

// ===== agent.Exchange =====

// Submit dispatches by order type. Limit orders are rejected in v1
// (no user-data-stream wiring yet); market orders delegate to the
// REST path in order.go.
func (e *Exchange) Submit(ctx context.Context, order agent.ExchangeOrder) (*agent.ExchangeSubmitResult, error) {
	switch order.OrderType {
	case "market":
		return e.client.SubmitMarket(ctx, order)
	case "limit":
		// v1 scope: limit orders need user-data-stream WS to surface
		// fills asynchronously. Reject with a stable reason so SaaS
		// strategies can detect the limitation in test_mode.
		return nil, fmt.Errorf("%w: limit_orders_not_supported_v1", agent.ErrExchangeRejected)
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
