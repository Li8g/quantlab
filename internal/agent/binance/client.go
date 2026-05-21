// Package binance implements an agent.Exchange backed by the Binance
// Spot REST API. The package targets both mainnet (https://api.binance.com)
// and testnet (https://testnet.binance.vision) — the only difference is
// the base URL passed at construction time, the wire protocol and
// signing scheme are identical.
//
// v1 scope (Step 1): REST client foundation only — signing, common
// error decoding, request lifecycle. Order placement / queries / the
// agent.Exchange impl land in later steps.
//
// Iron rule 3: API keys never leave this package via WS or logs; the
// signed() helper is the single chokepoint that touches the secret.
package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync/atomic"
	"time"
)

// BaseURL constants for the two Binance Spot environments. Choose via
// Options.BaseURL; defaults to mainnet so a misconfigured testnet
// agent fails loudly with auth errors rather than silently trading
// against fake balances.
const (
	BaseURLMainnet = "https://api.binance.com"
	BaseURLTestnet = "https://testnet.binance.vision"
)

// DefaultRecvWindowMs is the Binance-recommended value for signed
// requests. Binance rejects requests whose `timestamp + recvWindow`
// is in the past or whose `timestamp` is ahead of server clock by
// more than recvWindow. 5s is comfortable for hosts with NTP-synced
// clocks; later steps may add /api/v3/time-based drift correction.
const DefaultRecvWindowMs = 5000

// defaultHTTPTimeout caps an entire request including TLS handshake.
// 10s is generous for spot endpoints (Binance documents p99 < 1s)
// while still bounding hangs on dead sockets.
const defaultHTTPTimeout = 10 * time.Second

// userAgent identifies our process to Binance's edge. Some Binance
// edges have historically rejected requests with no User-Agent header.
const userAgent = "quantlab-agent/1.0"

// APIError is the typed error returned when Binance responds with a
// well-formed `{"code": -1234, "msg": "..."}` body on any non-2xx
// status. Caller can errors.As() to inspect Code (Binance's stable
// per-error int) and Message.
//
// The full Binance error code table is at
// https://binance-docs.github.io/apidocs/spot/en/#error-codes;
// downstream layers only need Code to distinguish retryable
// (e.g. -1003 too many requests) from terminal (e.g. -2010
// insufficient balance) failures.
type APIError struct {
	HTTPStatus int
	Code       int
	Message    string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("binance: http %d code=%d msg=%q",
		e.HTTPStatus, e.Code, e.Message)
}

// RateLimitError is returned on HTTP 429 (rate limit) or 418 (IP
// banned). Inner APIError is populated when Binance returned a
// well-formed `{code, msg}` body (typical); otherwise APIError.Code
// is zero and Message holds the trimmed body snippet.
//
// RetryAfter is parsed from the standard Retry-After response header.
// Zero means the header was missing — callers can treat this as
// "default backoff window" (recommended: at least 60s for 429, much
// longer for 418).
//
// Banned discriminates the two cases: 418 is a stronger signal that
// the IP is temporarily banned (Binance docs: minutes-to-days);
// callers should usually surface 418 as fatal and stop pushing
// requests rather than retrying.
type RateLimitError struct {
	APIError
	RetryAfter time.Duration
	Banned     bool
}

func (e *RateLimitError) Error() string {
	prefix := "rate_limited"
	if e.Banned {
		prefix = "ip_banned"
	}
	if e.RetryAfter > 0 {
		return fmt.Sprintf("binance: %s retry_after=%s (%s)",
			prefix, e.RetryAfter, e.APIError.Error())
	}
	return fmt.Sprintf("binance: %s (%s)", prefix, e.APIError.Error())
}

// Unwrap exposes the inner APIError so errors.As(*APIError) works
// against a RateLimitError too — useful for callers that only care
// about Code without distinguishing the rate-limit subclass.
func (e *RateLimitError) Unwrap() error { return &e.APIError }

// ErrEmptyAPIKey / ErrEmptyAPISecret guard signed() against silent
// misconfiguration. Public-only callers (Ping, BookTicker) can use
// NewPublicClient and skip the secret entirely.
var (
	ErrEmptyAPIKey    = errors.New("binance: api key empty")
	ErrEmptyAPISecret = errors.New("binance: api secret empty")
)

// Options tunes the Client. Zero values produce sensible defaults; the
// struct is value-type so callers do not need to think about lifetime.
type Options struct {
	// BaseURL overrides the endpoint. Empty → BaseURLMainnet. Use
	// BaseURLTestnet for the sandbox; same API surface, different
	// account namespace.
	BaseURL string

	// HTTPClient overrides the underlying http.Client. Tests inject a
	// *http.Client backed by httptest.NewServer. nil → a fresh client
	// with defaultHTTPTimeout.
	HTTPClient *http.Client

	// RecvWindowMs overrides the recvWindow query param on signed
	// requests. 0 → DefaultRecvWindowMs. Max enforced by Binance is
	// 60000.
	RecvWindowMs int

	// NowFn returns the current time. Tests inject a fixed clock so
	// the signature can be asserted byte-for-byte. nil → time.Now.
	NowFn func() time.Time
}

// Client is a concurrent-safe Binance Spot REST client. One per Agent
// process is sufficient.
type Client struct {
	baseURL      string
	apiKey       string
	apiSecret    []byte
	httpClient   *http.Client
	recvWindowMs int
	nowFn        func() time.Time

	// serverOffsetMs is added to the local clock when stamping
	// signed-request timestamps. Maintained by SyncTime; default 0
	// (treat local clock as authoritative). Atomic so the Exchange's
	// background re-sync goroutine can update concurrently with
	// in-flight signed requests.
	serverOffsetMs atomic.Int64

	// lastUsedWeight1m caches the most recent X-MBX-USED-WEIGHT-1m
	// header value (Binance's 1-minute rolling request-weight tally).
	// Updated by do() on every response that carries the header. Zero
	// means "no observation yet" (e.g. before any request, or
	// httptest backends that don't emit the header).
	lastUsedWeight1m atomic.Int32
}

// NewClient constructs a Client suitable for signed (account / order)
// endpoints. Empty apiKey / apiSecret are allowed at construction time
// — signed() will return ErrEmptyAPIKey / ErrEmptyAPISecret at call
// time so a process that only ever hits public endpoints can still
// boot.
func NewClient(apiKey, apiSecret string, opts Options) *Client {
	c := &Client{
		baseURL:      opts.BaseURL,
		apiKey:       apiKey,
		apiSecret:    []byte(apiSecret),
		httpClient:   opts.HTTPClient,
		recvWindowMs: opts.RecvWindowMs,
		nowFn:        opts.NowFn,
	}
	if c.baseURL == "" {
		c.baseURL = BaseURLMainnet
	}
	if c.httpClient == nil {
		c.httpClient = &http.Client{Timeout: defaultHTTPTimeout}
	}
	if c.recvWindowMs == 0 {
		c.recvWindowMs = DefaultRecvWindowMs
	}
	if c.nowFn == nil {
		c.nowFn = time.Now
	}
	return c
}

// BaseURL returns the configured endpoint. Tests and ops use it for
// log lines.
func (c *Client) BaseURL() string { return c.baseURL }

// unsigned issues a public request — no API key, no signature. Used
// for /api/v3/ping and /api/v3/ticker/bookTicker. params may be nil.
func (c *Client) unsigned(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	u := c.baseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("binance: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	return c.do(req)
}

// signed issues an authenticated request. timestamp + recvWindow are
// appended to params, the canonical-encoded query string is HMAC-SHA256
// signed with apiSecret, and the resulting signature is added as the
// final &signature= query parameter. X-MBX-APIKEY carries the public
// half of the credentials.
//
// Binance accepts SIGNED parameters in either the query string or the
// request body for POST; this implementation always uses query string
// for uniformity — the longest realistic /api/v3/order payload is
// ~250 bytes, well under the practical URL length limit.
func (c *Client) signed(ctx context.Context, method, path string, params url.Values) ([]byte, error) {
	if c.apiKey == "" {
		return nil, ErrEmptyAPIKey
	}
	if len(c.apiSecret) == 0 {
		return nil, ErrEmptyAPISecret
	}
	if params == nil {
		params = url.Values{}
	}
	params.Set("timestamp", strconv.FormatInt(
		c.nowFn().UnixMilli()+c.serverOffsetMs.Load(), 10))
	params.Set("recvWindow", strconv.Itoa(c.recvWindowMs))

	payload := params.Encode()
	mac := hmac.New(sha256.New, c.apiSecret)
	mac.Write([]byte(payload))
	sig := hex.EncodeToString(mac.Sum(nil))

	u := c.baseURL + path + "?" + payload + "&signature=" + sig
	req, err := http.NewRequestWithContext(ctx, method, u, nil)
	if err != nil {
		return nil, fmt.Errorf("binance: build signed request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", c.apiKey)
	req.Header.Set("User-Agent", userAgent)
	return c.do(req)
}

// do runs the HTTP exchange and normalises the response.
//   - 2xx: body bytes are returned to the caller.
//   - 429 (throttle) or 418 (IP banned): *RateLimitError (which is
//     also an *APIError via Unwrap).
//   - other non-2xx with a `{"code","msg"}` JSON body: *APIError.
//   - non-2xx with any other body: a generic error carrying the
//     status code and the first 256 bytes of body for triage.
//
// Every response (success or error) updates Client.lastUsedWeight1m
// from the X-MBX-USED-WEIGHT-1m header when present.
//
// Network errors / context cancellation propagate verbatim.
func (c *Client) do(req *http.Request) ([]byte, error) {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("binance: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("binance: read body: %w", err)
	}

	if v := resp.Header.Get("X-MBX-USED-WEIGHT-1M"); v != "" {
		if w, perr := strconv.Atoi(v); perr == nil && w >= 0 {
			c.lastUsedWeight1m.Store(int32(w))
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}

	apiErr, fromJSON := decodeAPIError(resp.StatusCode, body)

	if resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode == http.StatusTeapot { // 418 → Binance IP ban
		return nil, &RateLimitError{
			APIError:   apiErr,
			RetryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), c.nowFn()),
			Banned:     resp.StatusCode == http.StatusTeapot,
		}
	}

	if fromJSON {
		return nil, &apiErr
	}

	snippet := body
	if len(snippet) > 256 {
		snippet = snippet[:256]
	}
	return nil, fmt.Errorf("binance: http %d: %s", resp.StatusCode, string(snippet))
}

// decodeAPIError extracts {code, msg} from a Binance error body. The
// second return is true when the body was a well-formed {code, msg}
// JSON document (Binance's standard error shape); false when we fell
// back to a trimmed snippet (e.g. HTML edge errors, plain-text
// throttle bodies). Callers use the flag to decide between *APIError
// and a generic error for non-rate-limit responses — for 429/418 we
// always want the inner APIError populated even from a snippet.
func decodeAPIError(status int, body []byte) (APIError, bool) {
	var raw struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if jerr := json.Unmarshal(body, &raw); jerr == nil && raw.Msg != "" {
		return APIError{HTTPStatus: status, Code: raw.Code, Message: raw.Msg}, true
	}
	snippet := body
	if len(snippet) > 256 {
		snippet = snippet[:256]
	}
	return APIError{HTTPStatus: status, Code: 0, Message: string(snippet)}, false
}

// parseRetryAfter handles the two valid Retry-After header formats:
//   - delta-seconds: e.g. "60" → 60 * time.Second
//   - HTTP-date:     e.g. "Fri, 31 Dec 1999 23:59:59 GMT" → relative to now
//
// Returns 0 when the header is empty or unparseable — callers should
// treat 0 as "no hint" (use their own default backoff).
func parseRetryAfter(hdr string, now time.Time) time.Duration {
	if hdr == "" {
		return 0
	}
	if secs, err := strconv.Atoi(hdr); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(hdr); err == nil {
		if d := t.Sub(now); d > 0 {
			return d
		}
		return 0
	}
	return 0
}

// LastUsedWeight1m returns the most recent X-MBX-USED-WEIGHT-1m
// header value observed in any response from this Client. Zero means
// no observation yet. Binance's default Spot REQUEST_WEIGHT limit is
// 1200 per rolling minute; downstream callers can compare against
// their own threshold to decide whether to throttle.
func (c *Client) LastUsedWeight1m() int32 { return c.lastUsedWeight1m.Load() }

// ServerOffsetMs returns the cached server-vs-local clock skew (ms).
// Positive means Binance's clock is ahead of ours. Zero before the
// first SyncTime call. Reads are lock-free.
func (c *Client) ServerOffsetMs() int64 { return c.serverOffsetMs.Load() }

// SetServerOffsetMs forces the offset to a specific value. Production
// callers should use SyncTime; this entry point exists for tests that
// want to assert signing behaviour without standing up a /api/v3/time
// httptest endpoint.
func (c *Client) SetServerOffsetMs(off int64) { c.serverOffsetMs.Store(off) }

// SyncTime calls GET /api/v3/time and updates the cached offset using
// the midpoint of the request/response wall-clock window as the local
// reference. Result: signed-request timestamps approximate Binance's
// clock within ~RTT/2 even on hosts with drift the host NTP daemon
// hasn't caught up with.
//
// On error the cached offset is left untouched — a transient network
// failure must not regress a previously-synced clock to zero.
func (c *Client) SyncTime(ctx context.Context) error {
	t0 := c.nowFn().UnixMilli()
	body, err := c.unsigned(ctx, http.MethodGet, "/api/v3/time", nil)
	if err != nil {
		return fmt.Errorf("binance.SyncTime: %w", err)
	}
	t2 := c.nowFn().UnixMilli()
	var raw struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return fmt.Errorf("binance.SyncTime: decode: %w", err)
	}
	if raw.ServerTime <= 0 {
		return fmt.Errorf("binance.SyncTime: serverTime=%d not positive", raw.ServerTime)
	}
	// Assume symmetric network latency: at the moment the response
	// arrived (t2 local), server clock was approximately serverTime +
	// (t2 - t0) / 2. We want offset such that local + offset ≈ server,
	// so subtract the midpoint of the local window from serverTime.
	midpoint := (t0 + t2) / 2
	c.serverOffsetMs.Store(raw.ServerTime - midpoint)
	return nil
}
