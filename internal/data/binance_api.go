// binance_api.go: REST API fallback for K-lines that haven't been
// archived yet (typically the last 1-2 days).
//
// Spec (phase plan §三):
//   - Endpoint: GET https://api.binance.com/api/v3/klines
//   - Page size: limit ≤ 1000 (Binance hard cap)
//   - Spacing: ≥150ms between requests to stay under rate limits
//   - On 429 (rate limit) or 418 (IP ban warning): exponential backoff
//     with retry; respect the Retry-After header when present
//
// Pagination ("按时间倒推") is the orchestrator's responsibility (Phase
// 1.5-c). FetchKlines is a single-page primitive: one HTTP call, one
// page of rows, parsed and validated.
package data

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	// DefaultAPIBaseURL is the public spot REST endpoint.
	DefaultAPIBaseURL = "https://api.binance.com"

	// DefaultRequestInterval is the spacing between calls (phase plan §三).
	DefaultRequestInterval = 150 * time.Millisecond

	// DefaultMaxRetries caps retry attempts on 429/418 before giving up.
	DefaultMaxRetries = 5

	// DefaultBaseBackoff is the first retry's sleep; subsequent retries
	// double until DefaultMaxBackoff.
	DefaultBaseBackoff = 1 * time.Second
	DefaultMaxBackoff  = 60 * time.Second

	// klineMaxLimit is Binance's hard ceiling per /api/v3/klines request.
	klineMaxLimit = 1000

	// klineJSONCols is the expected column count per inner JSON array
	// (matches the CSV archive; the 12th is unused).
	klineJSONCols = 12
)

// APIClient calls the Binance public REST endpoint with throttling and
// 429/418 backoff. The zero value is NOT ready — use NewAPIClient.
//
// Concurrency: safe for concurrent FetchKlines calls; the rate-limit
// mutex serializes the pre-request wait.
type APIClient struct {
	BaseURL    string
	HTTPClient *http.Client

	RequestInterval time.Duration
	MaxRetries      int
	BaseBackoff     time.Duration
	MaxBackoff      time.Duration

	// nowFunc + sleep are injection points for deterministic tests.
	// Production code should leave them at default (time.Now / blocking
	// sleep). Tests can record sleeps and skip real waits.
	nowFunc func() time.Time
	sleep   func(ctx context.Context, d time.Duration) error

	mu      sync.Mutex
	lastReq time.Time
}

// NewAPIClient returns a client with phase-plan defaults.
func NewAPIClient() *APIClient {
	return &APIClient{
		BaseURL:         DefaultAPIBaseURL,
		HTTPClient:      &http.Client{Timeout: 30 * time.Second},
		RequestInterval: DefaultRequestInterval,
		MaxRetries:      DefaultMaxRetries,
		BaseBackoff:     DefaultBaseBackoff,
		MaxBackoff:      DefaultMaxBackoff,
		nowFunc:         time.Now,
		sleep:           contextAwareSleep,
	}
}

// FetchKlines returns up to limit (≤1000) klines in [startMs, endMs).
// Pass startMs=0 to omit start, endMs=0 to omit end (Binance returns
// the most recent klines in that case). Rows come back in ascending
// OpenTime order — same as the CSV archive — so callers can mix archive
// and API origins without reordering.
func (c *APIClient) FetchKlines(
	ctx context.Context,
	symbol, interval string,
	startMs, endMs int64,
	limit int,
) ([]KlineRow, error) {
	if symbol == "" || interval == "" {
		return nil, errors.New("api: symbol and interval are required")
	}
	if limit <= 0 || limit > klineMaxLimit {
		return nil, fmt.Errorf("api: limit=%d, must be in (0, %d]", limit, klineMaxLimit)
	}

	q := url.Values{}
	q.Set("symbol", symbol)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))
	if startMs > 0 {
		q.Set("startTime", strconv.FormatInt(startMs, 10))
	}
	if endMs > 0 {
		q.Set("endTime", strconv.FormatInt(endMs, 10))
	}
	endpoint := fmt.Sprintf("%s/api/v3/klines?%s",
		strings.TrimRight(c.BaseURL, "/"), q.Encode())

	body, err := c.doWithRetry(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	return parseKlinesJSON(body)
}

// doWithRetry handles rate limiting + 429/418 retry. Other error classes
// (network, 5xx, malformed body) propagate immediately — phase plan §三
// only mandates retry on 429/418.
func (c *APIClient) doWithRetry(ctx context.Context, endpoint string) ([]byte, error) {
	for attempt := 0; attempt <= c.MaxRetries; attempt++ {
		if err := c.waitRateLimit(ctx); err != nil {
			return nil, err
		}
		body, status, retryAfter, err := c.do(ctx, endpoint)
		c.markRequest()
		if err != nil {
			return nil, err
		}
		if status == http.StatusOK {
			return body, nil
		}
		if status != http.StatusTooManyRequests && status != http.StatusTeapot {
			return nil, fmt.Errorf("api: GET %s: HTTP %d (body: %s)", endpoint, status, truncate(body, 256))
		}
		if attempt == c.MaxRetries {
			return nil, fmt.Errorf("api: GET %s: HTTP %d after %d retries", endpoint, status, c.MaxRetries)
		}
		backoff := c.computeBackoff(attempt, retryAfter)
		if err := c.sleep(ctx, backoff); err != nil {
			return nil, err
		}
	}
	// Unreachable — the loop returns or errors before exhausting.
	return nil, errors.New("api: doWithRetry exhausted without decision")
}

// do executes one request and returns body, status, and the parsed
// Retry-After header (0 if absent/invalid). Only network/io errors are
// returned as `err`; HTTP-level outcomes go through status.
func (c *APIClient) do(ctx context.Context, endpoint string) (body []byte, status int, retryAfter time.Duration, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("api: build request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, 0, 0, fmt.Errorf("api: GET %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	body, err = io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, 0, fmt.Errorf("api: read body: %w", err)
	}
	retryAfter = parseRetryAfter(resp.Header.Get("Retry-After"))
	return body, resp.StatusCode, retryAfter, nil
}

func (c *APIClient) waitRateLimit(ctx context.Context) error {
	c.mu.Lock()
	delta := c.RequestInterval - c.nowFunc().Sub(c.lastReq)
	c.mu.Unlock()
	if delta <= 0 {
		return nil
	}
	return c.sleep(ctx, delta)
}

func (c *APIClient) markRequest() {
	c.mu.Lock()
	c.lastReq = c.nowFunc()
	c.mu.Unlock()
}

// computeBackoff: prefer the server's Retry-After when present, else
// exponential 2^attempt * BaseBackoff capped at MaxBackoff.
func (c *APIClient) computeBackoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		if retryAfter > c.MaxBackoff {
			return c.MaxBackoff
		}
		return retryAfter
	}
	d := time.Duration(math.Pow(2, float64(attempt))) * c.BaseBackoff
	if d > c.MaxBackoff {
		return c.MaxBackoff
	}
	return d
}

// parseRetryAfter handles both delta-seconds and HTTP-date forms; HTTP-
// date is rare for Binance so we keep it simple — integer seconds only.
func parseRetryAfter(h string) time.Duration {
	if h == "" {
		return 0
	}
	secs, err := strconv.Atoi(strings.TrimSpace(h))
	if err != nil || secs <= 0 {
		return 0
	}
	return time.Duration(secs) * time.Second
}

// contextAwareSleep blocks for d unless ctx fires first.
func contextAwareSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func truncate(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// parseKlinesJSON decodes the array-of-arrays kline response into
// []KlineRow. Each inner array has 12 columns matching the CSV order;
// numeric fields arrive as JSON strings (OHLC, volumes) or numbers
// (timestamps, num_trades).
func parseKlinesJSON(body []byte) ([]KlineRow, error) {
	var raw [][]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("api: json decode: %w", err)
	}
	out := make([]KlineRow, 0, len(raw))
	for i, rec := range raw {
		if len(rec) < klineCSVMinCols {
			return nil, fmt.Errorf("api: row %d: %d cols, need >= %d", i, len(rec), klineCSVMinCols)
		}
		strs := make([]string, klineJSONCols)
		for j := 0; j < klineCSVMinCols; j++ {
			s, err := normalizeJSONField(rec[j])
			if err != nil {
				return nil, fmt.Errorf("api: row %d col %d: %w", i, j, err)
			}
			strs[j] = s
		}
		row, err := parseKlineRow(strs)
		if err != nil {
			return nil, fmt.Errorf("api: row %d: %w", i, err)
		}
		out = append(out, row)
	}
	return out, nil
}

// normalizeJSONField turns a JSON value into the bare string that
// parseKlineRow expects (no surrounding quotes). Both string-form
// (`"0.123"`) and bare-number (`42`) inputs become `"0.123"` / `"42"`.
func normalizeJSONField(raw json.RawMessage) (string, error) {
	if len(raw) == 0 {
		return "", errors.New("empty field")
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", err
		}
		return s, nil
	}
	return string(raw), nil
}
