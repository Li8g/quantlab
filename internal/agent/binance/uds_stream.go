// uds_stream.go — User Data Stream WebSocket runtime for the Binance
// Spot adapter. Provides the OrderEventStreamer capability that the
// Agent's Client type-asserts at startup (see internal/agent/events.go).
//
// Goroutine model: one persistent goroutine driven by Exchange.Start.
// Lifecycle per session (WS API signature subscription — see
// userdatastream.go for why listenKey is gone):
//
//  1. wsconn.Dial(streamBaseURL)            — the WS API endpoint
//  2. WriteFrame(userDataStream.subscribe.signature) + read+verify ack
//  3. read event frames; unwrap envelope; decode `e` field; dispatch
//     executionReport → callback registered via Subscribe
//  4. on disconnect / serverShutdown event / decode error → close conn
//     + sleep with exponential backoff + go to step 1
//
// No application-level keepalive is needed: the subscription lives with
// the connection (closing the conn cancels it) and gorilla answers WS
// pings automatically. Binance closes the connection at the 24h mark;
// the reconnect loop re-subscribes transparently.
//
// REFACTOR HOOK — when adding outboundAccountPosition or balanceUpdate
// streams, decode them in the same `switch eventType` block. Surface
// them via a separate callback (not OrderEvent) or a richer event type
// — do not overload OrderEvent with balance-only data.
package binance

import (
	"context"
	"crypto/rand"
	"encoding/hex"
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

// WS API endpoints for the two Binance Spot environments. Auto-derived
// from BaseURL in NewExchange; callers can override via
// ExchangeOptions.StreamBaseURL when running against a non-standard
// gateway (proxies, regional mirrors).
const (
	WSAPIBaseURLMainnet = "wss://ws-api.binance.com/ws-api/v3"
	WSAPIBaseURLTestnet = "wss://ws-api.testnet.binance.vision/ws-api/v3"
)

// Default UDS tunings.
//
//   - DefaultUDSReconnectMin/Max: same shape as the agent's main
//     reconnect backoff. Capped at 60s — longer would mean stale
//     fill state on the SaaS side.
const (
	DefaultUDSReconnectMin = 1 * time.Second
	DefaultUDSReconnectMax = 60 * time.Second
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
	client        *Client
	streamBaseURL string
	reconnectMin  time.Duration
	reconnectMax  time.Duration
	dialer        UDSDialer
	logger        *slog.Logger

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
		client:        client,
		streamBaseURL: streamURL,
		reconnectMin:  rcMin,
		reconnectMax:  rcMax,
		dialer:        dialer,
		logger:        logger,
		stopCh:        make(chan struct{}),
		doneCh:        make(chan struct{}),
	}
}

// udsOptions captures the UDS-specific configuration. Held by the
// parent Exchange and threaded through NewExchange.
type udsOptions struct {
	StreamBaseURL string
	ReconnectMin  time.Duration
	ReconnectMax  time.Duration
	Dialer        UDSDialer
	Logger        *slog.Logger
}

// defaultStreamBaseURL maps the REST base URL to its paired WS API
// endpoint. Unknown hosts fall back to mainnet — better to fail loudly
// against the production gateway than to silently mis-route to the
// testnet.
func defaultStreamBaseURL(restBase string) string {
	switch restBase {
	case BaseURLTestnet:
		return WSAPIBaseURLTestnet
	case BaseURLMainnet, "":
		return WSAPIBaseURLMainnet
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

// Start launches the session loop. The loop dials, subscribes, runs one
// session until it errors or the stream signals serverShutdown, then
// reconnects with exponential backoff. Exits when ctx is cancelled or
// Close() is called.
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
//     capped at reconnectMax.
//   - Cancellation: ctx.Done OR stopCh terminates immediately, even
//     mid-session.
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

// runSession runs one UDS session end-to-end: dial the WS API endpoint,
// subscribe, then pump events. Returns nil only on clean shutdown
// signal; any other return indicates the caller should reconnect after
// backoff.
func (u *uds) runSession(ctx context.Context) error {
	dialCtx, dialCancel := context.WithTimeout(ctx, 30*time.Second)
	conn, err := u.dialer.Dial(dialCtx, u.streamBaseURL, nil)
	dialCancel()
	if err != nil {
		return fmt.Errorf("dial ws-api: %w", err)
	}
	defer func() { _ = conn.Close() }()

	if err := u.subscribe(ctx, conn); err != nil {
		return fmt.Errorf("subscribe: %w", err)
	}
	u.logger.Info("binance_uds_subscribed", "stream", u.streamBaseURL)

	sessionCtx, sessionCancel := context.WithCancel(ctx)
	defer sessionCancel()

	// Read pump runs in its own goroutine. select-watch readErrCh AND
	// stopCh AND ctx.
	readErrCh := make(chan error, 1)
	go u.readLoop(sessionCtx, conn, readErrCh)

	select {
	case err := <-readErrCh:
		return fmt.Errorf("read: %w", err)
	case <-ctx.Done():
		return ctx.Err()
	case <-u.stopCh:
		return nil
	}
}

// subscribe sends a signed userDataStream.subscribe.signature request
// and blocks for the ack frame, verifying status==200. Binance acks
// before streaming any events, so a synchronous read of the ack here is
// safe.
func (u *uds) subscribe(ctx context.Context, conn wsconn.Conn) error {
	req, err := u.client.buildSubscribeRequest(newWSRequestID())
	if err != nil {
		return err
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return fmt.Errorf("encode subscribe: %w", err)
	}

	wctx, wcancel := context.WithTimeout(ctx, 10*time.Second)
	err = conn.WriteFrame(wctx, raw)
	wcancel()
	if err != nil {
		return fmt.Errorf("write subscribe: %w", err)
	}

	rctx, rcancel := context.WithTimeout(ctx, 10*time.Second)
	frame, err := conn.ReadFrame(rctx)
	rcancel()
	if err != nil {
		return fmt.Errorf("read ack: %w", err)
	}

	var resp wsResponse
	if err := json.Unmarshal(frame, &resp); err != nil {
		return fmt.Errorf("decode ack: %w", err)
	}
	if resp.Status != 200 {
		if resp.Error != nil {
			return fmt.Errorf("subscribe rejected: status=%d code=%d msg=%q",
				resp.Status, resp.Error.Code, resp.Error.Msg)
		}
		return fmt.Errorf("subscribe rejected: status=%d", resp.Status)
	}
	return nil
}

// newWSRequestID returns a random hex id for a WS API request. Binance
// echoes it back in the matching response; uniqueness within a
// connection is all that's required.
func newWSRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failure is effectively impossible; fall back to a
		// time-based id rather than panicking the streamer.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b[:])
}

// readLoop drains frames from the conn. Each text frame is one event
// envelope per Binance docs. Decode → dispatch. On unrecoverable errors
// (conn closed, serverShutdown), pushes onto errCh and returns.
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
			// serverShutdown isn't a decode failure — it's a normal
			// terminal signal that we must reconnect.
			select {
			case errCh <- err:
			default:
			}
			return
		}
	}
}

// errServerShutdown signals that Binance is closing the connection.
// Caller restarts the session, which re-subscribes on a fresh conn.
var errServerShutdown = fmt.Errorf("serverShutdown")

// handleFrame unwraps one WS API frame and dispatches its inner event.
// Frames without an `event` field (a late response/ack) are ignored.
func (u *uds) handleFrame(frame []byte) error {
	var env wsEventEnvelope
	if err := json.Unmarshal(frame, &env); err != nil {
		// Don't kill the stream on decode error — log + skip.
		u.logger.Debug("binance_uds_decode_envelope_failed", "err", err)
		return nil
	}
	if len(env.Event) == 0 {
		u.logger.Debug("binance_uds_non_event_frame")
		return nil
	}
	return u.handleEvent(env.Event)
}

// handleEvent decodes one inner event payload and dispatches. Unknown
// event types are logged at debug — Binance ships occasional new event
// shapes that we don't need to surface (e.g. listStatus).
func (u *uds) handleEvent(payload []byte) error {
	// EventTime (`E`) is decoded only to give it a home: Go's JSON field
	// matching is case-insensitive, so without an `E` field the numeric
	// event-time value collides with EventType (`e`) and fails the
	// unmarshal. Real Binance events always carry `E`.
	var head struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
	}
	if err := json.Unmarshal(payload, &head); err != nil {
		u.logger.Debug("binance_uds_decode_head_failed", "err", err)
		return nil
	}
	switch head.EventType {
	case "executionReport":
		ev, ok, err := decodeExecutionReport(payload)
		if err != nil {
			u.logger.Warn("binance_uds_execution_report_decode_failed", "err", err)
			return nil
		}
		if ok {
			u.dispatch(ev)
		}
		return nil
	case "serverShutdown":
		u.logger.Info("binance_uds_server_shutdown")
		return errServerShutdown
	default:
		u.logger.Debug("binance_uds_unhandled_event", "type", head.EventType)
		return nil
	}
}

// rawExecutionReport mirrors the subset of executionReport fields the
// agent surfaces. Binance ships many more (icebergQty, makerCommission,
// quoteOrderQty, etc.) that we discard.
type rawExecutionReport struct {
	EventType string `json:"e"`
	// EventTime gives the numeric `E` field a home so it doesn't collide
	// with EventType under Go's case-insensitive JSON matching (real
	// Binance executionReport events always carry `E`). Unused otherwise.
	EventTime       int64  `json:"E"`
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
			return agent.ExchangeFill{}, fmt.Errorf("commission %q: %w", raw.Commission, err)
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
