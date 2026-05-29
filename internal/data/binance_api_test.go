package data

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// newTestClient returns an APIClient pointed at srv with no real sleeps;
// sleep calls are recorded so tests can assert backoff schedules without
// actually waiting.
func newTestClient(srv *httptest.Server) (*APIClient, *[]time.Duration) {
	calls := []time.Duration{}
	c := NewAPIClient()
	c.BaseURL = srv.URL
	c.RequestInterval = 0 // disable rate-limit waits in tests
	c.BaseBackoff = 10 * time.Millisecond
	c.MaxBackoff = 100 * time.Millisecond
	c.sleep = func(ctx context.Context, d time.Duration) error {
		calls = append(calls, d)
		if err := ctx.Err(); err != nil {
			return err
		}
		return nil
	}
	return c, &calls
}

const sampleKlineJSON = `[
  [1736294400000,"100000.00","100100.00","99900.00","100050.00","5.123",1736294459999,"512345.6",42,"3.0","300000.0","0"],
  [1736294460000,"100050.00","100200.00","100000.00","100150.00","4.500",1736294519999,"450500.0",38,"2.5","250000.0","0"]
]`

func TestAPIClient_FetchKlines_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v3/klines" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("symbol") != "BTCUSDT" {
			t.Errorf("symbol = %s", q.Get("symbol"))
		}
		if q.Get("interval") != "1m" {
			t.Errorf("interval = %s", q.Get("interval"))
		}
		if q.Get("limit") != "1000" {
			t.Errorf("limit = %s", q.Get("limit"))
		}
		if q.Get("startTime") != "1736294400000" {
			t.Errorf("startTime = %s", q.Get("startTime"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(sampleKlineJSON))
	}))
	defer srv.Close()

	c, _ := newTestClient(srv)
	rows, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 1736294400000, 0, 1000)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 {
		t.Fatalf("got %d rows, want 2", len(rows))
	}
	r0 := rows[0]
	if r0.OpenTime != 1736294400000 || r0.CloseTime != 1736294459999 {
		t.Errorf("row0 times: %+v", r0)
	}
	if r0.Open != 100000.0 || r0.NumTrades != 42 {
		t.Errorf("row0 fields: %+v", r0)
	}
}

func TestAPIClient_FetchKlines_BadInputs(t *testing.T) {
	c := NewAPIClient()
	cases := []struct {
		name             string
		symbol, interval string
		limit            int
	}{
		{"empty symbol", "", "1m", 100},
		{"empty interval", "BTCUSDT", "", 100},
		{"limit zero", "BTCUSDT", "1m", 0},
		{"limit too large", "BTCUSDT", "1m", 5000},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := c.FetchKlines(context.Background(), tc.symbol, tc.interval, 0, 0, tc.limit)
			if err == nil {
				t.Error("expected error")
			}
		})
	}
}

func TestAPIClient_Backoff_429_ThenSuccess(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&attempts, 1)
		if n < 3 {
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(sampleKlineJSON))
	}))
	defer srv.Close()

	c, sleeps := newTestClient(srv)
	rows, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 1000)
	if err != nil {
		t.Fatalf("expected success after retries: %v", err)
	}
	if len(rows) != 2 {
		t.Errorf("got %d rows, want 2", len(rows))
	}
	if atomic.LoadInt32(&attempts) != 3 {
		t.Errorf("attempts = %d, want 3", attempts)
	}
	// Two backoff sleeps expected (after attempt 0 and 1, before success on 2).
	// Exponential: 10ms, 20ms (BaseBackoff * 2^attempt).
	if len(*sleeps) != 2 {
		t.Fatalf("got %d sleeps, want 2: %v", len(*sleeps), *sleeps)
	}
	if (*sleeps)[0] != 10*time.Millisecond {
		t.Errorf("sleep[0] = %v, want 10ms", (*sleeps)[0])
	}
	if (*sleeps)[1] != 20*time.Millisecond {
		t.Errorf("sleep[1] = %v, want 20ms", (*sleeps)[1])
	}
}

func TestAPIClient_Backoff_RespectsRetryAfter(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(sampleKlineJSON))
	}))
	defer srv.Close()

	c, sleeps := newTestClient(srv)
	c.MaxBackoff = 60 * time.Second // make sure Retry-After fits
	if _, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 100); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 3*time.Second {
		t.Errorf("sleeps = %v, want [3s]", *sleeps)
	}
}

func TestAPIClient_Backoff_RetryAfterCappedAtMax(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9999")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, sleeps := newTestClient(srv) // MaxBackoff = 100ms in test client
	c.MaxRetries = 1
	_, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 100)
	if err == nil {
		t.Fatal("expected error after retries exhausted")
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != c.MaxBackoff {
		t.Errorf("sleeps = %v, want [%v]", *sleeps, c.MaxBackoff)
	}
}

func TestAPIClient_MaxRetriesExceeded(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, _ := newTestClient(srv)
	c.MaxRetries = 3
	_, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 429") {
		t.Errorf("err = %q", err.Error())
	}
	// MaxRetries=3 → 4 total attempts (initial + 3 retries).
	if got := atomic.LoadInt32(&attempts); got != 4 {
		t.Errorf("attempts = %d, want 4", got)
	}
}

func TestAPIClient_NonRetryableErrorImmediate(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&attempts, 1)
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()

	c, sleeps := newTestClient(srv)
	_, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 100)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("err = %q", err.Error())
	}
	if atomic.LoadInt32(&attempts) != 1 {
		t.Errorf("attempts = %d, want 1 (no retry on 5xx)", attempts)
	}
	if len(*sleeps) != 0 {
		t.Errorf("sleeps = %v, want none", *sleeps)
	}
}

func TestAPIClient_418Retries(t *testing.T) {
	var attempts int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&attempts, 1) == 1 {
			w.WriteHeader(http.StatusTeapot) // 418 — Binance IP ban warning
			return
		}
		_, _ = w.Write([]byte(sampleKlineJSON))
	}))
	defer srv.Close()

	c, _ := newTestClient(srv)
	if _, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 100); err != nil {
		t.Fatalf("expected retry on 418, got %v", err)
	}
	if atomic.LoadInt32(&attempts) != 2 {
		t.Errorf("attempts = %d, want 2", attempts)
	}
}

func TestAPIClient_RateLimitWait(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()

	c, sleeps := newTestClient(srv)
	c.RequestInterval = 150 * time.Millisecond

	// Fake a clock starting at t=0; lastReq starts at zero value.
	now := time.Unix(1_000_000, 0)
	c.nowFunc = func() time.Time { return now }

	// First request: lastReq is zero → no wait.
	if _, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 0 {
		t.Errorf("first call slept %v, want none", *sleeps)
	}

	// Second request 50ms later → must wait 100ms more.
	now = now.Add(50 * time.Millisecond)
	if _, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 1 || (*sleeps)[0] != 100*time.Millisecond {
		t.Errorf("second-call sleeps = %v, want [100ms]", *sleeps)
	}

	// Third request 300ms later → no wait (interval already elapsed).
	now = now.Add(300 * time.Millisecond)
	if _, err := c.FetchKlines(context.Background(), "BTCUSDT", "1m", 0, 0, 1); err != nil {
		t.Fatal(err)
	}
	if len(*sleeps) != 1 {
		t.Errorf("third call added a sleep, want none: %v", *sleeps)
	}
}

func TestAPIClient_ContextCancelDuringBackoff(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	c, _ := newTestClient(srv)
	// Override sleep to return ctx.Err() when the context is cancelled.
	c.sleep = func(ctx context.Context, d time.Duration) error {
		<-ctx.Done()
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()
	_, err := c.FetchKlines(ctx, "BTCUSDT", "1m", 0, 0, 100)
	if err == nil {
		t.Fatal("expected context-cancel error")
	}
	if !errorsIs(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestParseKlinesJSON_Direct(t *testing.T) {
	rows, err := parseKlinesJSON([]byte(sampleKlineJSON))
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[1].OpenTime != 1736294460000 {
		t.Errorf("rows: %+v", rows)
	}
}

func TestParseKlinesJSON_RejectsCorrupt(t *testing.T) {
	cases := []string{
		`not json`,
		`{}`,
		`[[1,2,3]]`, // too few cols
		`[["NOT_A_NUMBER","x","y","z","w","v",1,"q",1,"a","b","0"]]`, // bad number
	}
	for i, body := range cases {
		t.Run(fmt.Sprintf("case%d", i), func(t *testing.T) {
			if _, err := parseKlinesJSON([]byte(body)); err == nil {
				t.Errorf("expected error for %q", body)
			}
		})
	}
}

func TestParseRetryAfter(t *testing.T) {
	cases := []struct {
		in   string
		want time.Duration
	}{
		{"", 0},
		{"5", 5 * time.Second},
		{"  10 ", 10 * time.Second},
		{"0", 0},
		{"-1", 0},
		{"not_a_number", 0},
	}
	for _, c := range cases {
		t.Run(strconv.Quote(c.in), func(t *testing.T) {
			got := parseRetryAfter(c.in)
			if got != c.want {
				t.Errorf("got %v, want %v", got, c.want)
			}
		})
	}
}

// errorsIs is a tiny shim so the test file does not need the errors
// import when only one usage exists.
func errorsIs(err, target error) bool { return err == target }
