// uds_stream.go — User Data Stream WebSocket runtime for the Binance
// Spot adapter. Provides the OrderEventStreamer capability that the
// Agent's Client type-asserts at startup (see internal/agent/events.go).
//
// Goroutine model: one persistent goroutine driven by Exchange.Start.
// Lifecycle per session:
//
//  1. Client.CreateListenKey()  (REST)
//  2. wsconn.Dial(streamBaseURL + "/ws/" + listenKey)
//  3. spawn keepalive ticker — Client.KeepaliveListenKey() every
//     UDSKeepaliveInterval (default 30min)
//  4. read frames; decode `e` field; dispatch executionReport →
//     callback registered via Subscribe
//  5. on disconnect / listenKeyExpired event / decode error → close
//     conn + sleep with exponential backoff + go to step 1
//
// REFACTOR HOOK — when adding outboundAccountPosition or balanceUpdate
// streams, decode them in the same `switch eventType` block. Surface
// them via a separate callback (not OrderEvent) or a richer event type
// — do not overload OrderEvent with balance-only data.
package binance

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
	"quantlab/internal/wire"
	"quantlab/internal/wsconn"
)

// Stream URL pairs for the two Binance Spot environments. Auto-derived
// from BaseURL in NewExchange; callers can override via
// ExchangeOptions.StreamBaseURL when running against a non-standard
// gateway (proxies, regional mirrors).
const (
	StreamBaseURLMainnet = "wss://stream.binance.com:9443"
	StreamBaseURLTestnet = "wss://stream.testnet.binance.vision"
)

// Default UDS tunings.
//
//   - DefaultUDSKeepaliveInterval: half of Binance's 60-minute expiry
//     ceiling so a single dropped keepalive doesn't kill the stream.
//   - DefaultUDSReconnectMin/Max: same shape as the agent's main
//     reconnect backoff. Capped at 60s — longer would mean stale
//     fill state on the SaaS side.
const (
	DefaultUDSKeepaliveInterval = 30 * time.Minute
	DefaultUDSReconnectMin      = 1 * time.Second
	DefaultUDSReconnectMax      = 60 * time.Second
)

// UDSDialer abstracts the WS dial so tests can substitute a pipe
// without spinning up a real WS server. The production impl is
// wsconn.Dial — same as the Agent client side.
type UDSDialer interface {
	Dial(ctx context.Context, url string, header http.Header) (wsconn.Conn, error)
}

// gorillaUDSDialer adapts wsconn.Dial to the UDSDialer interface.
type gorillaUDSDialer struct{}

func (gorillaUDSDialer) Dial(ctx context.Context, url string, header http.Header) (wsconn.Conn, error) {
	return wsconn.Dial(ctx, url, header)
}

// uds runs the User Data Stream goroutine. One per Exchange. Methods
// are not safe for concurrent use except as documented; the streamer
// goroutine is the only writer/reader for most fields.
type uds struct {
	client            *Client
	streamBaseURL     string
	keepaliveInterval time.Duration
	reconnectMin      time.Duration
	reconnectMax      time.Duration
	dialer            UDSDialer
	logger            *slog.Logger

	// cbMu guards cb so Subscribe and dispatch can race. cb is
	// last-wins replacement per the OrderEventStreamer contract.
	cbMu sync.Mutex
	cb   func(agent.OrderEvent)

	// stopOnce protects the closeCh against double-close.
	stopOnce sync.Once
	stopCh   chan struct{}
	doneCh   chan struct{}
}

// newUDS constructs the runtime. Callers must Start() to begin event
// processing; the constructor itself does no IO.
func newUDS(client *Client, opts udsOptions) *uds {
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	streamURL := opts.StreamBaseURL
	if streamURL == "" {
		streamURL = defaultStreamBaseURL(client.BaseURL())
	}
	keepalive := opts.KeepaliveInterval
	if keepalive <= 0 {
		keepalive = DefaultUDSKeepaliveInterval
	}
	rcMin := opts.ReconnectMin
	if rcMin <= 0 {
		rcMin = DefaultUDSReconnectMin
	}
	rcMax := opts.ReconnectMax
	if rcMax <= 0 {
		rcMax = DefaultUDSReconnectMax
	}
	if rcMax < rcMin {
		rcMax = rcMin
	}
	dialer := opts.Dialer
	if dialer == nil {
		dialer = gorillaUDSDialer{}
	}
	return &uds{
		client:            client,
		streamBaseURL:     streamURL,
		keepaliveInterval: keepalive,
		reconnectMin:      rcMin,
		reconnectMax:      rcMax,
		dialer:            dialer,
		logger:            logger,
		stopCh:            make(chan struct{}),
		doneCh:            make(chan struct{}),
	}
}

// udsOptions captures the UDS-specific configuration. Held by the
// parent Exchange and threaded through NewExchange.
type udsOptions struct {
	StreamBaseURL     string
	KeepaliveInterval time.Duration
	ReconnectMin      time.Duration
	ReconnectMax      time.Duration
	Dialer            UDSDialer
	Logger            *slog.Logger
}

// defaultStreamBaseURL maps the REST base URL to its paired stream
// host. Unknown hosts fall back to mainnet — better to fail loudly
// against the production gateway than to silently mis-route to the
// testnet.
func defaultStreamBaseURL(restBase string) string {
	switch restBase {
	case BaseURLTestnet:
		return StreamBaseURLTestnet
	case BaseURLMainnet, "":
		return StreamBaseURLMainnet
	}
	// Test fixtures (httptest.Server) and non-standard mirrors: leave
	// untouched. NewExchange callers should set StreamBaseURL
	// explicitly in that case.
	return ""
}

// Subscribe implements agent.OrderEventStreamer. Last-wins
// replacement: a second Subscribe replaces the first callback.
func (u *uds) Subscribe(cb func(agent.OrderEvent)) {
	u.cbMu.Lock()
	defer u.cbMu.Unlock()
	u.cb = cb
}

// dispatch invokes the registered callback if any. Safe to call from
// any goroutine.
func (u *uds) dispatch(ev agent.OrderEvent) {
	u.cbMu.Lock()
	cb := u.cb
	u.cbMu.Unlock()
	if cb == nil {
		return
	}
	cb(ev)
}

// Start launches the session loop. The loop dials, runs one session
// until it errors or the stream signals listenKeyExpired, then
// reconnects with exponential backoff. Exits when ctx is cancelled
// or Close() is called.
func (u *uds) Start(ctx context.Context) {
	go u.run(ctx)
}

// Close stops the loop and waits for the goroutine to exit. Safe to
// call before Start (the doneCh is closed by the constructor path
// only if Start ran). Idempotent.
func (u *uds) Close() {
	u.stopOnce.Do(func() {
		close(u.stopCh)
	})
}

// Wait blocks until the runtime goroutine exits. Called from
// Exchange.Close after Close().
func (u *uds) Wait() { <-u.doneCh }

// run is the main session loop. Implements:
//
//   - Backoff: starts at reconnectMin, doubles after each failure,
//     capped at reconnectMax. Resets on successful Ready.
//   - Cancellation: ctx.Done OR stopCh terminates immediately, even
//     mid-session.
//   - listenKey lifecycle: CloseListenKey on session end (best effort).
func (u *uds) run(ctx context.Context) {
	defer close(u.doneCh)

	backoff := u.reconnectMin
	for {
		// Cancellation gate before each attempt.
		if ctx.Err() != nil {
			return
		}
		select {
		case <-u.stopCh:
			return
		default:
		}

		err := u.runSession(ctx)
		if ctx.Err() != nil || isStopped(u.stopCh) {
			return
		}
		if err != nil {
			u.logger.Warn("binance_uds_session_error", "err", err, "backoff", backoff)
		}

		// Sleep with backoff, then double up to reconnectMax.
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return
		case <-u.stopCh:
			return
		}
		backoff *= 2
		if backoff > u.reconnectMax {
			backoff = u.reconnectMax
		}
	}
}

// runSession runs one UDS session end-to-end. Returns nil only on
// clean shutdown signal; any other return indicates the caller should
// reconnect after backoff.
func (u *uds) runSession(ctx context.Context) error {
	listenKey, err := u.createKeyWithTimeout(ctx)
	if err != nil {
		return fmt.Errorf("createListenKey: %w", err)
	}
	defer u.closeKeyBestEffort(ctx, listenKey)

	url := u.streamBaseURL + "/ws/" + listenKey
	dialCtx, dialCancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := u.dialer.Dial(dialCtx, url, nil)
	dialCancel()
	if err != nil {
		return fmt.Errorf("dial uds: %w", err)
	}
	defer func() { _ = conn.Close() }()

	u.logger.Info("binance_uds_connected", "stream", u.streamBaseURL)

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Keepalive ticker. PUT failures bubble back as session restart —
	// either the key has expired (Binance returns 400/404) or the
	// edge is unreachable; both warrant a fresh listenKey.
	keepaliveErrCh := make(chan error, 1)
	go u.keepaliveLoop(sessionCtx, listenKey, keepaliveErrCh)

	// Read pump runs in this goroutine. select-watch keepaliveErrCh
	// AND stopCh AND a per-frame read result.
	readErrCh := make(chan error, 1)
	go u.readLoop(sessionCtx, conn, readErrCh)

	select {
	case err := <-readErrCh:
		return fmt.Errorf("read: %w", err)
	case err := <-keepaliveErrCh:
		return fmt.Errorf("keepalive: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	case <-u.stopCh:
		return nil
	}
}

// createKeyWithTimeout wraps CreateListenKey with a 10s timeout. The
// REST call usually completes in <100ms; 10s leaves room for cold
// edges.
func (u *uds) createKeyWithTimeout(ctx context.Context) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return u.client.CreateListenKey(cctx)
}

// closeKeyBestEffort calls CloseListenKey with a short timeout and
// swallows errors. The key will time out on its own within 60min so
// this is a hygiene measure, not a correctness one.
func (u *uds) closeKeyBestEffort(ctx context.Context, listenKey string) {
	cctx, cancel := context.WithTimeout(detach(ctx), 5*time.Second)
	defer cancel()
	if err := u.client.CloseListenKey(cctx, listenKey); err != nil {
		u.logger.Debug("binance_uds_close_key_best_effort", "err", err)
	}
}

// detach returns a fresh context with the parent's values but no
// cancellation propagation — used so closeKeyBestEffort runs even
// when ctx was the reason we're tearing down.
func detach(ctx context.Context) context.Context {
	return context.WithoutCancel(ctx)
}

// keepaliveLoop sends a PUT every keepaliveInterval. On the first
// failure, it pushes the error onto errCh and returns; the session
// loop will restart with a fresh listenKey.
func (u *uds) keepaliveLoop(ctx context.Context, listenKey string, errCh chan<- error) {
	t := time.NewTicker(u.keepaliveInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-u.stopCh:
			return
		case <-t.C:
			kctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := u.client.KeepaliveListenKey(kctx, listenKey)
			cancel()
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
				return
			}
		}
	}
}

// readLoop drains frames from the conn. Each text frame is one event
// per Binance docs. Decode → dispatch. On unrecoverable errors (conn
// closed, decode failures repeatedly), pushes onto errCh and returns.
//
// A listenKeyExpired event is treated as a normal disconnect (push a
// sentinel error). Production callers see it via the logger.
func (u *uds) readLoop(ctx context.Context, conn wsconn.Conn, errCh chan<- error) {
	for {
		frame, err := conn.ReadFrame(ctx)
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
			return
		}
		if err := u.handleFrame(frame); err != nil {
			// listenKeyExpired isn't a decode failure — it's a normal
			// terminal signal that we must rotate the key.
			select {
			case errCh <- err:
			default:
			}
			return
		}
	}
}

// errListenKeyExpired signals that Binance has rotated us off the
// current listenKey. Caller restarts the session, which calls
// CreateListenKey for a fresh one.
var errListenKeyExpired = fmt.Errorf("listenKeyExpired")

// handleFrame decodes one raw frame and dispatches. Unknown event
// types are logged at debug — Binance ships occasional new event
// shapes that we don't need to surface (e.g. listStatus).
func (u *uds) handleFrame(frame []byte) error {
	var head struct {
		EventType string `json:"e"`
	}
	if err := json.Unmarshal(frame, &head); err != nil {
		// Don't kill the stream on decode error — log + skip.
		u.logger.Debug("binance_uds_decode_head_failed", "err", err)
		return nil
	}
	switch head.EventType {
	case "executionReport":
		ev, ok, err := decodeExecutionReport(frame)
		if err != nil {
			u.logger.Warn("binance_uds_execution_report_decode_failed", "err", err)
			return nil
		}
		if ok {
			u.dispatch(ev)
		}
		return nil
	case "listenKeyExpired":
		u.logger.Info("binance_uds_listen_key_expired")
		return errListenKeyExpired
	default:
		u.logger.Debug("binance_uds_unhandled_event", "type", head.EventType)
		return nil
	}
}

// rawExecutionReport mirrors the subset of executionReport fields the
// agent surfaces. Binance ships many more (icebergQty, makerCommission,
// quoteOrderQty, etc.) that we discard.
type rawExecutionReport struct {
	EventType       string `json:"e"`
	Symbol          string `json:"s"`
	ClientOrderID   string `json:"c"`
	Side            string `json:"S"`
	OrderType       string `json:"o"`
	ExecutionType   string `json:"x"` // NEW/CANCELED/REPLACED/REJECTED/TRADE/EXPIRED
	OrderStatus     string `json:"X"` // NEW/PARTIALLY_FILLED/FILLED/CANCELED/EXPIRED/REJECTED
	OrderID         int64  `json:"i"`
	LastFillQty     string `json:"l"`
	LastFillPrice   string `json:"L"`
	Commission      string `json:"n"`
	CommissionAsset string `json:"N"`
	TransactTime    int64  `json:"T"`
	CumulativeQty   string `json:"z"`
}

// decodeExecutionReport maps one executionReport JSON to an
// OrderEvent. Returns (ev, true, nil) for events the Agent surfaces;
// (_, false, nil) for events we deliberately drop (e.g. status=NEW
// is already covered by the synchronous Submit Ack); (_, _, err) for
// hard decode failures.
//
// Mapping rules (Binance → wire):
//
//   - executionType=TRADE          → OrderEvent.Fill populated;
//     Status = partial_filled OR filled (per orderStatus).
//   - executionType=CANCELED       → Status=cancelled, Fill=nil.
//   - executionType=REJECTED       → Status=rejected, Fill=nil.
//   - executionType=EXPIRED        → Status=cancelled (treat as
//     non-terminal cancellation for SaaS purposes).
//   - executionType=NEW/REPLACED   → dropped (NEW is already in the
//     synchronous Ack; REPLACED is out of v1 scope).
func decodeExecutionReport(frame []byte) (agent.OrderEvent, bool, error) {
	var raw rawExecutionReport
	if err := json.Unmarshal(frame, &raw); err != nil {
		return agent.OrderEvent{}, false, fmt.Errorf("decode executionReport: %w", err)
	}
	if raw.ClientOrderID == "" {
		return agent.OrderEvent{}, false, fmt.Errorf("executionReport missing client_order_id (c)")
	}

	ev := agent.OrderEvent{
		ClientOrderID:   raw.ClientOrderID,
		ExchangeOrderID: strconv.FormatInt(raw.OrderID, 10),
		Side:            strings.ToLower(raw.Side),
	}

	switch strings.ToUpper(raw.ExecutionType) {
	case "TRADE":
		fill, err := buildFillFromExecution(raw)
		if err != nil {
			return agent.OrderEvent{}, false, err
		}
		ev.Fill = &fill
		if raw.CumulativeQty != "" {
			if cum, err := decimal.NewFromString(raw.CumulativeQty); err == nil {
				ev.CumulativeFillQuantity = cum
			}
		}
		// Per orderStatus distinguish partial vs final fill.
		switch strings.ToUpper(raw.OrderStatus) {
		case "FILLED":
			ev.Status = wire.OrderStatusFilled
		case "PARTIALLY_FILLED":
			ev.Status = wire.OrderStatusPartialFilled
		default:
			// Defensive — Binance shouldn't emit TRADE with NEW status,
			// but if it does, default to partial.
			ev.Status = wire.OrderStatusPartialFilled
		}
		return ev, true, nil
	case "CANCELED":
		ev.Status = wire.OrderStatusCancelled
		return ev, true, nil
	case "REJECTED":
		ev.Status = wire.OrderStatusRejected
		return ev, true, nil
	case "EXPIRED":
		// SaaS doesn't distinguish exchange-side time expiry from cancel;
		// surface as cancelled to keep the wire enum tight.
		ev.Status = wire.OrderStatusCancelled
		return ev, true, nil
	default:
		// NEW / REPLACED / others — drop.
		return agent.OrderEvent{}, false, nil
	}
}

// buildFillFromExecution converts the executionReport fill fields to
// agent.ExchangeFill. All decimal parsing must succeed — partial
// parse would mean a corrupt event we can't safely report.
func buildFillFromExecution(raw rawExecutionReport) (agent.ExchangeFill, error) {
	qty, err := decimal.NewFromString(raw.LastFillQty)
	if err != nil {
		return agent.ExchangeFill{}, fmt.Errorf("LastFillQty %q: %w", raw.LastFillQty, err)
	}
	price, err := decimal.NewFromString(raw.LastFillPrice)
	if err != nil {
		return agent.ExchangeFill{}, fmt.Errorf("LastFillPrice %q: %w", raw.LastFillPrice, err)
	}
	// Commission may be empty when there is no fee for this fill
	// (rare on Spot but possible for promo accounts); treat empty as
	// zero rather than failing.
	fee := decimal.Zero
	if raw.Commission != "" {
		fee, err = decimal.NewFromString(raw.Commission)
		if err != nil {
			return agent.ExchangeFill{}, fmt.Errorf("Commission %q: %w", raw.Commission, err)
		}
	}
	return agent.ExchangeFill{
		FillQuantity:       qty,
		FillPrice:          price,
		FillFeeAsset:       raw.CommissionAsset,
		FillFeeAmount:      fee,
		FilledAtExchangeMs: raw.TransactTime,
	}, nil
}

// isStopped reports whether stopCh has been closed. Lock-free.
func isStopped(stopCh chan struct{}) bool {
	select {
	case <-stopCh:
		return true
	default:
		return false
	}
}
