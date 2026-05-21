package binance

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

// fixedNow returns a deterministic time so the signature is stable
// across test runs. The value is past Binance's launch so any
// downstream sanity check on "timestamp not too old" passes.
var fixedNow = func() time.Time { return time.UnixMilli(1700000000000) }

// newTestClient pairs a Client with a captured-request httptest.Server.
// The handler closure can inspect or reply to one request per test.
func newTestClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
		NowFn:      fixedNow,
	})
	return c, srv
}

func TestNewClient_AppliesDefaults(t *testing.T) {
	c := NewClient("k", "s", Options{})
	if c.BaseURL() != BaseURLMainnet {
		t.Errorf("BaseURL = %q, want mainnet default", c.BaseURL())
	}
	if c.recvWindowMs != DefaultRecvWindowMs {
		t.Errorf("recvWindowMs = %d, want %d", c.recvWindowMs, DefaultRecvWindowMs)
	}
	if c.httpClient == nil {
		t.Error("httpClient nil after NewClient")
	}
	if c.nowFn == nil {
		t.Error("nowFn nil after NewClient")
	}
}

func TestUnsigned_NoAuthHeaderNoSignature(t *testing.T) {
	var gotPath, gotQuery, gotAuth, gotUA string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("X-MBX-APIKEY")
		gotUA = r.Header.Get("User-Agent")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil); err != nil {
		t.Fatalf("unsigned: %v", err)
	}
	if gotPath != "/api/v3/ping" {
		t.Errorf("path = %q", gotPath)
	}
	if gotQuery != "" {
		t.Errorf("unsigned request should have no query, got %q", gotQuery)
	}
	if gotAuth != "" {
		t.Errorf("unsigned request leaked X-MBX-APIKEY = %q", gotAuth)
	}
	if gotUA != userAgent {
		t.Errorf("User-Agent = %q, want %q", gotUA, userAgent)
	}
}

func TestUnsigned_AppendsParamsAsQuery(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`[]`))
	})
	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	if _, err := c.unsigned(context.Background(), http.MethodGet,
		"/api/v3/ticker/bookTicker", params); err != nil {
		t.Fatalf("unsigned: %v", err)
	}
	if gotQuery != "symbol=BTCUSDT" {
		t.Errorf("query = %q, want symbol=BTCUSDT", gotQuery)
	}
}

func TestSigned_AddsTimestampRecvWindowAndSignature(t *testing.T) {
	var gotQuery, gotAuth string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("X-MBX-APIKEY")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.signed(context.Background(), http.MethodGet, "/api/v3/account", nil); err != nil {
		t.Fatalf("signed: %v", err)
	}
	if gotAuth != "PUBKEY" {
		t.Errorf("X-MBX-APIKEY = %q, want PUBKEY", gotAuth)
	}

	// Required query params for any signed call.
	for _, key := range []string{"timestamp=", "recvWindow=", "signature="} {
		if !strings.Contains(gotQuery, key) {
			t.Errorf("query %q missing %s", gotQuery, key)
		}
	}
}

func TestSigned_SignatureMatchesHMACSHA256OverCanonicalPayload(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	if _, err := c.signed(context.Background(), http.MethodPost, "/api/v3/order", params); err != nil {
		t.Fatalf("signed: %v", err)
	}

	// Reconstruct: everything before "&signature=" is the canonical
	// payload that was signed. Verify HMAC-SHA256(payload, secret) ==
	// signature.
	idx := strings.LastIndex(gotQuery, "&signature=")
	if idx == -1 {
		t.Fatalf("no &signature= in query: %s", gotQuery)
	}
	payload := gotQuery[:idx]
	gotSig := gotQuery[idx+len("&signature="):]

	mac := hmac.New(sha256.New, []byte("SECRET"))
	mac.Write([]byte(payload))
	wantSig := hex.EncodeToString(mac.Sum(nil))
	if gotSig != wantSig {
		t.Errorf("signature mismatch:\n got=%s\nwant=%s\npayload=%s",
			gotSig, wantSig, payload)
	}
}

func TestSigned_PreservesCallerParams(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	params.Set("side", "BUY")
	if _, err := c.signed(context.Background(), http.MethodPost, "/api/v3/order", params); err != nil {
		t.Fatalf("signed: %v", err)
	}
	// url.Values.Encode sorts keys alphabetically; symbol > side
	// alphabetically does NOT hold (side < symbol), so the order is
	// side, symbol — but we don't pin order, only presence.
	if !strings.Contains(gotQuery, "symbol=BTCUSDT") {
		t.Errorf("query missing symbol=BTCUSDT: %s", gotQuery)
	}
	if !strings.Contains(gotQuery, "side=BUY") {
		t.Errorf("query missing side=BUY: %s", gotQuery)
	}
}

func TestSigned_TimestampMatchesNowFn(t *testing.T) {
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.signed(context.Background(), http.MethodGet, "/api/v3/account", nil); err != nil {
		t.Fatalf("signed: %v", err)
	}
	parsed, err := url.ParseQuery(gotQuery)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if got := parsed.Get("timestamp"); got != "1700000000000" {
		t.Errorf("timestamp = %q, want 1700000000000 (fixedNow.UnixMilli)", got)
	}
}

func TestSigned_ErrorsOnEmptyAPIKey(t *testing.T) {
	c := NewClient("", "SECRET", Options{
		BaseURL: "http://unreachable",
		NowFn:   fixedNow,
	})
	_, err := c.signed(context.Background(), http.MethodGet, "/api/v3/account", nil)
	if !errors.Is(err, ErrEmptyAPIKey) {
		t.Errorf("err = %v, want ErrEmptyAPIKey", err)
	}
}

func TestSigned_ErrorsOnEmptyAPISecret(t *testing.T) {
	c := NewClient("PUBKEY", "", Options{
		BaseURL: "http://unreachable",
		NowFn:   fixedNow,
	})
	_, err := c.signed(context.Background(), http.MethodGet, "/api/v3/account", nil)
	if !errors.Is(err, ErrEmptyAPISecret) {
		t.Errorf("err = %v, want ErrEmptyAPISecret", err)
	}
}

func TestDo_Returns2xxBody(t *testing.T) {
	want := `{"serverTime":1700000000123}`
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(want))
	})
	got, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/time", nil)
	if err != nil {
		t.Fatalf("unsigned: %v", err)
	}
	if string(got) != want {
		t.Errorf("body = %q, want %q", string(got), want)
	}
}

func TestDo_ParsesBinanceErrorJSON(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ticker/bookTicker", nil)
	if err == nil {
		t.Fatal("want error on 400 response")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.HTTPStatus != 400 {
		t.Errorf("HTTPStatus = %d, want 400", apiErr.HTTPStatus)
	}
	if apiErr.Code != -1121 {
		t.Errorf("Code = %d, want -1121", apiErr.Code)
	}
	if apiErr.Message != "Invalid symbol." {
		t.Errorf("Message = %q", apiErr.Message)
	}
}

func TestDo_NonBinanceErrorBody(t *testing.T) {
	// 5xx without a {code, msg} payload (e.g. an edge HTML page) must
	// surface a generic error with the body snippet, not crash decoding.
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(502)
		_, _ = w.Write([]byte("<html>Bad Gateway</html>"))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil)
	if err == nil {
		t.Fatal("want error on 502")
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		t.Errorf("err unexpectedly *APIError: %v", err)
	}
	if !strings.Contains(err.Error(), "502") || !strings.Contains(err.Error(), "Bad Gateway") {
		t.Errorf("err = %v, want generic msg containing status + body", err)
	}
}

func TestDo_TrimsLongErrorBodySnippet(t *testing.T) {
	long := strings.Repeat("A", 1024)
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(long))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil)
	if err == nil {
		t.Fatal("want error")
	}
	// Snippet capped at 256 chars; error string therefore < 350 chars total.
	if len(err.Error()) > 350 {
		t.Errorf("error too long (%d chars), snippet not trimmed: %s",
			len(err.Error()), err.Error())
	}
}

func TestDo_PropagatesContextCancel(t *testing.T) {
	// Handler sleeps longer than the test will wait, so ctx must fire.
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := c.unsigned(ctx, http.MethodGet, "/api/v3/ping", nil)
	if err == nil {
		t.Fatal("want error on context cancellation")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want context.DeadlineExceeded", err)
	}
}

// ===== Phase 7.9: rate-limit awareness =====

func TestDo_ParsesUsedWeightHeader(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("X-MBX-USED-WEIGHT-1m", "742")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil); err != nil {
		t.Fatalf("unsigned: %v", err)
	}
	if got := c.LastUsedWeight1m(); got != 742 {
		t.Errorf("LastUsedWeight1m = %d, want 742", got)
	}
}

func TestDo_HandlesMissingUsedWeightHeader(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil); err != nil {
		t.Fatalf("unsigned: %v", err)
	}
	if got := c.LastUsedWeight1m(); got != 0 {
		t.Errorf("LastUsedWeight1m = %d, want 0 (no observation)", got)
	}
}

func TestDo_RateLimit429ReturnsRateLimitError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.Header().Set("X-MBX-USED-WEIGHT-1m", "1201")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"code":-1003,"msg":"Too many requests."}`))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil)
	if err == nil {
		t.Fatal("want error on 429")
	}
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rlErr.Banned {
		t.Error("Banned = true, want false (429 is throttle, not ban)")
	}
	if rlErr.RetryAfter != 60*time.Second {
		t.Errorf("RetryAfter = %s, want 60s", rlErr.RetryAfter)
	}
	if rlErr.Code != -1003 {
		t.Errorf("inner APIError.Code = %d, want -1003", rlErr.Code)
	}
	// errors.As(*APIError) must still succeed via Unwrap.
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Error("errors.As(*APIError) failed against RateLimitError")
	}
	if got := c.LastUsedWeight1m(); got != 1201 {
		t.Errorf("LastUsedWeight1m = %d, want 1201 (header parsed even on 429)", got)
	}
}

func TestDo_RateLimit418ReturnsRateLimitErrorBanned(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "300")
		w.WriteHeader(418)
		_, _ = w.Write([]byte(`{"code":-1003,"msg":"WAY too many requests; IP banned."}`))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil)
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if !rlErr.Banned {
		t.Error("Banned = false, want true (418 → IP ban)")
	}
	if rlErr.RetryAfter != 300*time.Second {
		t.Errorf("RetryAfter = %s, want 5min", rlErr.RetryAfter)
	}
}

func TestDo_RateLimit429NoBodyStillRateLimitError(t *testing.T) {
	// Some Binance edges return 429 with a plain-text body. Verify
	// RateLimitError is still produced (Banned=false, RetryAfter parsed,
	// inner APIError.Message carries the snippet).
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "10")
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`rate limited`))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil)
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rlErr.RetryAfter != 10*time.Second {
		t.Errorf("RetryAfter = %s, want 10s", rlErr.RetryAfter)
	}
	if rlErr.Message != "rate limited" {
		t.Errorf("inner Message = %q, want trimmed snippet", rlErr.Message)
	}
}

func TestDo_RateLimitMissingRetryAfterHeader(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(429)
		_, _ = w.Write([]byte(`{"code":-1003,"msg":"slow down"}`))
	})
	_, err := c.unsigned(context.Background(), http.MethodGet, "/api/v3/ping", nil)
	var rlErr *RateLimitError
	if !errors.As(err, &rlErr) {
		t.Fatalf("err = %v, want *RateLimitError", err)
	}
	if rlErr.RetryAfter != 0 {
		t.Errorf("RetryAfter = %s, want 0 (no header)", rlErr.RetryAfter)
	}
}

func TestParseRetryAfter_HTTPDate(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	later := now.Add(45 * time.Second).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(later, now); got != 45*time.Second {
		t.Errorf("HTTP-date: got %s, want 45s", got)
	}
}

func TestParseRetryAfter_PastDateClampsZero(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Hour).UTC().Format(http.TimeFormat)
	if got := parseRetryAfter(past, now); got != 0 {
		t.Errorf("past HTTP-date: got %s, want 0", got)
	}
}

func TestParseRetryAfter_Garbage(t *testing.T) {
	if got := parseRetryAfter("not-a-time", time.Now()); got != 0 {
		t.Errorf("garbage: got %s, want 0", got)
	}
	if got := parseRetryAfter("", time.Now()); got != 0 {
		t.Errorf("empty: got %s, want 0", got)
	}
}

func TestRateLimitError_ErrorString(t *testing.T) {
	rl := &RateLimitError{
		APIError:   APIError{HTTPStatus: 429, Code: -1003, Message: "too many"},
		RetryAfter: 5 * time.Second,
		Banned:     false,
	}
	if !strings.Contains(rl.Error(), "rate_limited") || !strings.Contains(rl.Error(), "5s") {
		t.Errorf("Error() = %q, want rate_limited + 5s", rl.Error())
	}
	rl.Banned = true
	if !strings.Contains(rl.Error(), "ip_banned") {
		t.Errorf("Error() = %q, want ip_banned for Banned=true", rl.Error())
	}
}

func TestSigned_QueryStringMatchesSignedPayloadByte(t *testing.T) {
	// Defends the invariant: whatever we sign is EXACTLY what we send.
	// If we re-encode params between signing and sending the server's
	// signature check fails. This test catches that regression class.
	var gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})

	params := url.Values{}
	params.Set("symbol", "BTCUSDT")
	params.Set("quantity", "0.001")
	if _, err := c.signed(context.Background(), http.MethodPost, "/api/v3/order", params); err != nil {
		t.Fatalf("signed: %v", err)
	}

	// gotQuery is RawQuery exactly as received. Strip "&signature=..."
	// and we should have the byte-identical canonical payload that the
	// HMAC was computed over.
	idx := strings.LastIndex(gotQuery, "&signature=")
	if idx == -1 {
		t.Fatalf("no signature in %s", gotQuery)
	}
	payload := gotQuery[:idx]

	mac := hmac.New(sha256.New, []byte("SECRET"))
	mac.Write([]byte(payload))
	wantSig := hex.EncodeToString(mac.Sum(nil))
	gotSig := gotQuery[idx+len("&signature="):]
	if gotSig != wantSig {
		t.Errorf("server-side signature recomputation failed:\n got=%s\nwant=%s\npayload=%s",
			gotSig, wantSig, payload)
	}
}
