package binance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

// exchangeHandler tracks per-endpoint hit counts so tests can assert
// the background ping loop, MarketRef capture, and order POST without
// needing a sleep-based oracle.
type exchangeHandler struct {
	pings   atomic.Int32
	books   atomic.Int32
	orders  atomic.Int32
	account atomic.Int32

	// Replies per endpoint. nil → 200 with a default body.
	pingReply    http.HandlerFunc
	bookReply    http.HandlerFunc
	orderReply   http.HandlerFunc
	accountReply http.HandlerFunc
}

func (h *exchangeHandler) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v3/ping":
		h.pings.Add(1)
		if h.pingReply != nil {
			h.pingReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	case "/api/v3/ticker/bookTicker":
		h.books.Add(1)
		if h.bookReply != nil {
			h.bookReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"symbol":"BTCUSDT",
			"bidPrice":"49990.00","bidQty":"1",
			"askPrice":"50010.00","askQty":"1"
		}`))
	case "/api/v3/order":
		h.orders.Add(1)
		if h.orderReply != nil {
			h.orderReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"orderId":42,
			"transactTime":1714000000000,
			"status":"FILLED",
			"fills":[{"price":"50010","qty":"0.001","commission":"0.05","commissionAsset":"USDT"}]
		}`))
	case "/api/v3/account":
		h.account.Add(1)
		if h.accountReply != nil {
			h.accountReply(w, r)
			return
		}
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"balances":[{"asset":"BTC","free":"1.0","locked":"0"}]}`))
	default:
		http.NotFound(w, r)
	}
}

func newExchangeFixture(t *testing.T, h *exchangeHandler, pingInterval time.Duration) (*Exchange, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(srv.Close)
	ex := NewExchange("PUBKEY", "SECRET", ExchangeOptions{
		BaseURL:      srv.URL,
		HTTPClient:   srv.Client(),
		NowFn:        fixedNow,
		PingInterval: pingInterval,
	})
	t.Cleanup(func() { _ = ex.Close() })
	return ex, srv
}

// waitForCount blocks until counter is at least n or the deadline
// passes. Returns final value (so failed asserts can log it).
func waitForCount(c *atomic.Int32, n int32, timeout time.Duration) int32 {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Load() >= n {
			return c.Load()
		}
		time.Sleep(2 * time.Millisecond)
	}
	return c.Load()
}

// ===== interface + scope =====

func TestExchange_SatisfiesAgentExchange(t *testing.T) {
	// Already enforced by `var _ agent.Exchange = (*Exchange)(nil)` in
	// exchange.go — this test makes the contract explicit so a
	// regression also fails here (next to the test rather than the
	// type assertion deep in a file).
	var _ agent.Exchange = NewExchange("k", "s", ExchangeOptions{})
}

func TestExchange_LimitOrderRejected(t *testing.T) {
	h := &exchangeHandler{
		orderReply: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("/api/v3/order must not be called for limit orders")
			w.WriteHeader(200)
		},
	}
	ex, _ := newExchangeFixture(t, h, 10*time.Hour) // disable ping in this test
	_, err := ex.Submit(context.Background(), agent.ExchangeOrder{
		OrderType:     "limit",
		Symbol:        "BTCUSDT",
		Side:          "buy",
		Quantity:      decimal.RequireFromString("0.001"),
		LimitPrice:    decimal.RequireFromString("50000"),
		ClientOrderID: "01HKLIMIT00000000000000001",
	})
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Fatalf("err = %v, want errors.Is ErrExchangeRejected", err)
	}
	if !strings.Contains(err.Error(), "limit_orders_not_supported_v1") {
		t.Errorf("err = %v, want stable reason", err)
	}
}

func TestExchange_UnknownOrderTypeRejected(t *testing.T) {
	ex, _ := newExchangeFixture(t, &exchangeHandler{}, 10*time.Hour)
	_, err := ex.Submit(context.Background(), agent.ExchangeOrder{
		OrderType: "stop_loss",
		Symbol:    "BTCUSDT",
	})
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("err = %v, want ErrExchangeRejected", err)
	}
}

// ===== market happy path =====

func TestExchange_SubmitMarket_DelegatesToOrderEndpoint(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 10*time.Hour)
	res, err := ex.Submit(context.Background(), agent.ExchangeOrder{
		OrderType:     "market",
		Symbol:        "BTCUSDT",
		Side:          "buy",
		Quantity:      decimal.RequireFromString("0.001"),
		ClientOrderID: "01HKMKT000000000000000001",
	})
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	if res.ExchangeOrderID != "42" {
		t.Errorf("ExchangeOrderID = %q, want 42", res.ExchangeOrderID)
	}
	if h.books.Load() != 1 || h.orders.Load() != 1 {
		t.Errorf("books=%d orders=%d, want 1/1", h.books.Load(), h.orders.Load())
	}
}

// ===== Positions delegation =====

func TestExchange_Positions_DelegatesToAccount(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 10*time.Hour)
	pos, err := ex.Positions(context.Background())
	if err != nil {
		t.Fatalf("Positions: %v", err)
	}
	if h.account.Load() != 1 {
		t.Errorf("account calls = %d, want 1", h.account.Load())
	}
	if len(pos) != 1 || pos[0].Symbol != "BTC" {
		t.Errorf("pos = %+v, want one BTC entry", pos)
	}
}

// ===== Reachable / ping loop =====

func TestExchange_Reachable_FalseBeforeStart(t *testing.T) {
	ex, _ := newExchangeFixture(t, &exchangeHandler{}, 10*time.Hour)
	if ex.Reachable() {
		t.Error("Reachable should be false before Start")
	}
}

func TestExchange_Reachable_TrueAfterSuccessfulPing(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)

	if got := waitForCount(&h.pings, 1, 1*time.Second); got < 1 {
		t.Fatalf("ping never observed; pings=%d", got)
	}
	// Reachable transition lags Ping completion by a microsecond at most.
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		if ex.Reachable() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("Reachable still false after %d successful pings", h.pings.Load())
}

func TestExchange_Reachable_FalseAfterPingFailure(t *testing.T) {
	h := &exchangeHandler{
		pingReply: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(503)
			_, _ = w.Write([]byte(`{"code":-1001,"msg":"server is down"}`))
		},
	}
	ex, _ := newExchangeFixture(t, h, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)

	if got := waitForCount(&h.pings, 1, 1*time.Second); got < 1 {
		t.Fatalf("ping never observed; pings=%d", got)
	}
	// Give the goroutine a moment to update reachable after the failure.
	time.Sleep(20 * time.Millisecond)
	if ex.Reachable() {
		t.Errorf("Reachable = true after failed Ping")
	}
}

func TestExchange_PingLoop_TicksAtInterval(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)

	// Expect ≥3 pings within 200ms: one initial + ≥2 ticker fires.
	if got := waitForCount(&h.pings, 3, 500*time.Millisecond); got < 3 {
		t.Errorf("ping count = %d, want ≥3", got)
	}
}

// ===== lifecycle =====

func TestExchange_Start_IsIdempotent(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 30*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ex.Start(ctx)
	ex.Start(ctx) // second call no-ops
	ex.Start(ctx)

	// Only ONE goroutine should be running — if multiple started,
	// we'd see ping count jump by ≥6 instead of ~3 over the same window.
	time.Sleep(120 * time.Millisecond)
	got := h.pings.Load()
	if got < 2 {
		t.Errorf("ping count = %d, want ≥2 (loop running)", got)
	}
	if got > 6 {
		t.Errorf("ping count = %d, want ≤6 (single loop only)", got)
	}
}

func TestExchange_Close_BeforeStart_NoDeadlock(t *testing.T) {
	ex, _ := newExchangeFixture(t, &exchangeHandler{}, 10*time.Hour)
	done := make(chan struct{})
	go func() {
		_ = ex.Close()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close() blocked forever when Start was never called")
	}
}

func TestExchange_Close_StopsPingLoop(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)

	// Let the loop tick a couple of times.
	if got := waitForCount(&h.pings, 2, 500*time.Millisecond); got < 2 {
		t.Fatalf("ping count = %d, want ≥2 before Close", got)
	}

	if err := ex.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}

	// After Close, the count should freeze (allow one in-flight tick).
	frozen := h.pings.Load()
	time.Sleep(80 * time.Millisecond)
	if delta := h.pings.Load() - frozen; delta > 1 {
		t.Errorf("ping count grew by %d after Close (want 0-1)", delta)
	}
}

func TestExchange_Close_IsIdempotent(t *testing.T) {
	ex, _ := newExchangeFixture(t, &exchangeHandler{}, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)
	_ = ex.Close()
	_ = ex.Close() // panicked-double-close on stopCh would fail here
	_ = ex.Close()
}

func TestExchange_CtxCancel_StopsPingLoop(t *testing.T) {
	h := &exchangeHandler{}
	ex, _ := newExchangeFixture(t, h, 20*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	ex.Start(ctx)

	if got := waitForCount(&h.pings, 2, 500*time.Millisecond); got < 2 {
		t.Fatalf("ping count = %d, want ≥2", got)
	}

	cancel()
	// Wait for goroutine to exit naturally.
	time.Sleep(50 * time.Millisecond)
	frozen := h.pings.Load()
	time.Sleep(80 * time.Millisecond)
	if delta := h.pings.Load() - frozen; delta > 0 {
		t.Errorf("ping count grew by %d after ctx cancel", delta)
	}
}
