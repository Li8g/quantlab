package binance

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

// orderHandler routes BookTicker + /api/v3/order from one httptest
// server. The book closure returns the JSON for /ticker/bookTicker;
// the order closure inspects the incoming POST and returns the order
// JSON. Either may write a non-200 status to exercise error paths.
type orderHandler struct {
	bookCalls  atomic.Int32
	orderCalls atomic.Int32
	book       http.HandlerFunc
	order      http.HandlerFunc
}

func (h *orderHandler) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v3/ticker/bookTicker":
		h.bookCalls.Add(1)
		h.book(w, r)
	case "/api/v3/order":
		h.orderCalls.Add(1)
		h.order(w, r)
	default:
		http.NotFound(w, r)
	}
}

func newOrderTestClient(t *testing.T, h *orderHandler) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(srv.Close)
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
		NowFn:      fixedNow,
	})
	return c, srv
}

// fxOrder returns a valid market-buy ExchangeOrder for use in
// happy-path tests. Tests that need a different side / quantity copy
// and mutate.
func fxOrder() agent.ExchangeOrder {
	return agent.ExchangeOrder{
		ClientOrderID: "01HKCOID000000000000000099",
		Symbol:        "BTCUSDT",
		Side:          "buy",
		OrderType:     "market",
		Quantity:      decimal.RequireFromString("0.001"),
	}
}

// fxLimitOrder returns a valid limit-buy ExchangeOrder. LimitPrice is
// set well below typical market for symmetry with fxOrder; happy-path
// tests can rely on the order not crossing the book in the mocked
// response.
func fxLimitOrder() agent.ExchangeOrder {
	return agent.ExchangeOrder{
		ClientOrderID: "01HKLIM000000000000000099",
		Symbol:        "BTCUSDT",
		Side:          "buy",
		OrderType:     "limit",
		Quantity:      decimal.RequireFromString("0.001"),
		LimitPrice:    decimal.RequireFromString("45000.00"),
	}
}

func bookHandlerJSON(bid, ask string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"symbol":"BTCUSDT",
			"bidPrice":"` + bid + `",
			"bidQty":"1",
			"askPrice":"` + ask + `",
			"askQty":"1"
		}`))
	}
}

// ===== happy-path =====

func TestSubmitMarket_HappyPath_SingleFill(t *testing.T) {
	var orderQuery string
	h := &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, r *http.Request) {
			orderQuery = r.URL.RawQuery
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"symbol":"BTCUSDT",
				"orderId":42,
				"transactTime":1714000000123,
				"status":"FILLED",
				"fills":[
					{"price":"50012.34","qty":"0.001","commission":"0.05001234","commissionAsset":"USDT","tradeId":1}
				]
			}`))
		},
	}
	c, _ := newOrderTestClient(t, h)
	res, err := c.SubmitMarket(context.Background(), fxOrder())
	if err != nil {
		t.Fatalf("SubmitMarket: %v", err)
	}

	if h.bookCalls.Load() != 1 {
		t.Errorf("bookCalls = %d, want 1 (MarketRef must be captured before order)", h.bookCalls.Load())
	}
	if h.orderCalls.Load() != 1 {
		t.Errorf("orderCalls = %d, want 1", h.orderCalls.Load())
	}

	if res.ExchangeOrderID != "42" {
		t.Errorf("ExchangeOrderID = %q, want 42", res.ExchangeOrderID)
	}
	if res.AcceptedAtMs != 1714000000123 {
		t.Errorf("AcceptedAtMs = %d", res.AcceptedAtMs)
	}
	// market buy → MarketRef = ask
	if !res.MarketRef.Equal(decimal.RequireFromString("50010.00")) {
		t.Errorf("MarketRef = %s, want 50010.00 (ask for buy)", res.MarketRef)
	}
	if len(res.Fills) != 1 {
		t.Fatalf("len(fills) = %d, want 1", len(res.Fills))
	}
	f := res.Fills[0]
	if !f.FillQuantity.Equal(decimal.RequireFromString("0.001")) {
		t.Errorf("FillQuantity = %s", f.FillQuantity)
	}
	if !f.FillPrice.Equal(decimal.RequireFromString("50012.34")) {
		t.Errorf("FillPrice = %s", f.FillPrice)
	}
	if !f.FillFeeAmount.Equal(decimal.RequireFromString("0.05001234")) {
		t.Errorf("FillFeeAmount = %s", f.FillFeeAmount)
	}
	if f.FillFeeAsset != "USDT" {
		t.Errorf("FillFeeAsset = %q", f.FillFeeAsset)
	}
	if f.FilledAtExchangeMs != 1714000000123 {
		t.Errorf("FilledAtExchangeMs = %d (must inherit transactTime)", f.FilledAtExchangeMs)
	}

	// Verify the POST carried the expected params on the wire.
	parsed, err := url.ParseQuery(orderQuery)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	for k, want := range map[string]string{
		"symbol":           "BTCUSDT",
		"side":             "BUY",
		"type":             "MARKET",
		"quantity":         "0.001",
		"newClientOrderId": "01HKCOID000000000000000099",
		"newOrderRespType": "FULL",
	} {
		if got := parsed.Get(k); got != want {
			t.Errorf("query.%s = %q, want %q", k, got, want)
		}
	}
	if parsed.Get("timestamp") == "" || parsed.Get("signature") == "" {
		t.Errorf("signed-request params missing in %s", orderQuery)
	}
}

func TestSubmitMarket_HappyPath_MultiFill(t *testing.T) {
	h := &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"symbol":"BTCUSDT",
				"orderId":43,
				"transactTime":1714000000200,
				"status":"FILLED",
				"fills":[
					{"price":"50010.00","qty":"0.0006","commission":"0.030006","commissionAsset":"USDT","tradeId":2},
					{"price":"50020.00","qty":"0.0004","commission":"0.020008","commissionAsset":"USDT","tradeId":3}
				]
			}`))
		},
	}
	c, _ := newOrderTestClient(t, h)
	res, err := c.SubmitMarket(context.Background(), fxOrder())
	if err != nil {
		t.Fatalf("SubmitMarket: %v", err)
	}
	if len(res.Fills) != 2 {
		t.Fatalf("len(fills) = %d, want 2", len(res.Fills))
	}
	totalQty := decimal.Zero
	for _, f := range res.Fills {
		totalQty = totalQty.Add(f.FillQuantity)
	}
	if !totalQty.Equal(decimal.RequireFromString("0.001")) {
		t.Errorf("sum(fills.qty) = %s, want 0.001", totalQty)
	}
	if res.Fills[0].FilledAtExchangeMs != res.Fills[1].FilledAtExchangeMs {
		t.Errorf("multi-fill timestamps diverged: %v", res.Fills)
	}
}

func TestSubmitMarket_SellUsesBidAsMarketRef(t *testing.T) {
	h := &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"symbol":"BTCUSDT",
				"orderId":44,
				"transactTime":1714000000300,
				"status":"FILLED",
				"fills":[
					{"price":"49985.00","qty":"0.001","commission":"0.05","commissionAsset":"USDT","tradeId":4}
				]
			}`))
		},
	}
	c, _ := newOrderTestClient(t, h)
	order := fxOrder()
	order.Side = "sell"
	res, err := c.SubmitMarket(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitMarket: %v", err)
	}
	if !res.MarketRef.Equal(decimal.RequireFromString("49990.00")) {
		t.Errorf("MarketRef = %s, want 49990.00 (bid for sell)", res.MarketRef)
	}
}

func TestSubmitMarket_AcceptsUppercaseSide(t *testing.T) {
	h := &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, r *http.Request) {
			if got := r.URL.Query().Get("side"); got != "BUY" {
				t.Errorf("side query = %q, want BUY", got)
			}
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"orderId":45,
				"transactTime":1,
				"fills":[]
			}`))
		},
	}
	c, _ := newOrderTestClient(t, h)
	order := fxOrder()
	order.Side = "BUY"
	_, err := c.SubmitMarket(context.Background(), order)
	if err != nil {
		t.Fatalf("SubmitMarket: %v", err)
	}
}

// ===== validation =====

func TestSubmitMarket_RejectsNonMarketOrderType(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("BookTicker should not be called for non-market order")
			w.WriteHeader(200)
		},
		order: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("/api/v3/order should not be called")
			w.WriteHeader(200)
		},
	})
	order := fxOrder()
	order.OrderType = "limit"
	_, err := c.SubmitMarket(context.Background(), order)
	if err == nil {
		t.Fatal("want error on limit OrderType")
	}
	if !strings.Contains(err.Error(), "market") {
		t.Errorf("err = %v, want mention of 'market'", err)
	}
}

func TestSubmitMarket_RejectsZeroQuantity(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book:  func(w http.ResponseWriter, _ *http.Request) { t.Error("should not be called") },
		order: func(w http.ResponseWriter, _ *http.Request) { t.Error("should not be called") },
	})
	order := fxOrder()
	order.Quantity = decimal.Zero
	_, err := c.SubmitMarket(context.Background(), order)
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Errorf("err = %v, want 'must be positive'", err)
	}
}

func TestSubmitMarket_RejectsNegativeQuantity(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book:  func(w http.ResponseWriter, _ *http.Request) { t.Error("should not be called") },
		order: func(w http.ResponseWriter, _ *http.Request) { t.Error("should not be called") },
	})
	order := fxOrder()
	order.Quantity = decimal.RequireFromString("-1")
	_, err := c.SubmitMarket(context.Background(), order)
	if err == nil || !strings.Contains(err.Error(), "positive") {
		t.Errorf("err = %v, want 'must be positive'", err)
	}
}

func TestSubmitMarket_RejectsEmptySymbol(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{})
	order := fxOrder()
	order.Symbol = ""
	_, err := c.SubmitMarket(context.Background(), order)
	if err == nil || !strings.Contains(err.Error(), "symbol") {
		t.Errorf("err = %v, want 'empty symbol'", err)
	}
}

func TestSubmitMarket_RejectsEmptyClientOrderID(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{})
	order := fxOrder()
	order.ClientOrderID = ""
	_, err := c.SubmitMarket(context.Background(), order)
	if err == nil || !strings.Contains(err.Error(), "client_order_id") {
		t.Errorf("err = %v, want 'empty client_order_id'", err)
	}
}

func TestSubmitMarket_RejectsUnknownSide(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book:  func(w http.ResponseWriter, _ *http.Request) { t.Error("should not be called") },
		order: func(w http.ResponseWriter, _ *http.Request) { t.Error("should not be called") },
	})
	order := fxOrder()
	order.Side = "short"
	_, err := c.SubmitMarket(context.Background(), order)
	if err == nil || !strings.Contains(err.Error(), "invalid side") {
		t.Errorf("err = %v, want 'invalid side'", err)
	}
}

// ===== error mapping =====

func TestSubmitMarket_BinanceAPIError_WrapsErrExchangeRejected(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":-2010,"msg":"Account has insufficient balance for requested action."}`))
		},
	})
	_, err := c.SubmitMarket(context.Background(), fxOrder())
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("err = %v, want errors.Is ErrExchangeRejected", err)
	}
	// Reason text should mention the Binance code so the SaaS Ack
	// reject_reason is actionable.
	if !strings.Contains(err.Error(), "-2010") {
		t.Errorf("err = %v, want -2010 in message", err)
	}
}

func TestSubmitMarket_RateLimit429WrapsErrExchangeRejectedWithReason(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "30")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"code":-1003,"msg":"Too many requests."}`))
		},
	})
	_, err := c.SubmitMarket(context.Background(), fxOrder())
	if err == nil {
		t.Fatal("want error")
	}
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("err = %v, want errors.Is ErrExchangeRejected", err)
	}
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("err = %v, want reason 'rate_limited'", err)
	}
	if !strings.Contains(err.Error(), "30s") {
		t.Errorf("err = %v, want retry_after=30s", err)
	}
}

func TestSubmitMarket_IPBanned418WrapsErrExchangeRejectedWithReason(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "600")
			w.WriteHeader(418)
			_, _ = w.Write([]byte(`{"code":-1003,"msg":"IP banned"}`))
		},
	})
	_, err := c.SubmitMarket(context.Background(), fxOrder())
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("err = %v, want errors.Is ErrExchangeRejected", err)
	}
	if !strings.Contains(err.Error(), "ip_banned") {
		t.Errorf("err = %v, want reason 'ip_banned'", err)
	}
}

func TestSubmitMarket_BookTickerFailureNotWrapped(t *testing.T) {
	// Network-shape failure on MarketRef capture: surfaces as a raw
	// error so the caller can distinguish "order definitely not sent"
	// from "exchange said no".
	c, _ := newOrderTestClient(t, &orderHandler{
		book: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(503)
			_, _ = w.Write([]byte("upstream unavailable"))
		},
		order: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("/api/v3/order must not be called when BookTicker fails")
			w.WriteHeader(200)
		},
	})
	_, err := c.SubmitMarket(context.Background(), fxOrder())
	if err == nil {
		t.Fatal("want error")
	}
	if errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("network-shape failure should NOT wrap ErrExchangeRejected; err = %v", err)
	}
	if !strings.Contains(err.Error(), "MarketRef") {
		t.Errorf("err = %v, want mention of 'MarketRef'", err)
	}
}

func TestSubmitMarket_MissingOrderID(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"orderId":0,"transactTime":1,"fills":[]}`))
		},
	})
	_, err := c.SubmitMarket(context.Background(), fxOrder())
	if err == nil || !strings.Contains(err.Error(), "orderId") {
		t.Errorf("err = %v, want 'missing orderId'", err)
	}
}

func TestSubmitMarket_MalformedFillDecimal(t *testing.T) {
	c, _ := newOrderTestClient(t, &orderHandler{
		book: bookHandlerJSON("49990.00", "50010.00"),
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"orderId":46,
				"transactTime":1714000000,
				"fills":[
					{"price":"NaN","qty":"0.001","commission":"0","commissionAsset":"USDT"}
				]
			}`))
		},
	})
	_, err := c.SubmitMarket(context.Background(), fxOrder())
	if err == nil || !strings.Contains(err.Error(), "fill[0].price") {
		t.Errorf("err = %v, want 'fill[0].price'", err)
	}
}

// ===== Phase 7.10: SubmitLimit =====

// orderLimitOnly is a minimal handler for the limit-order REST path —
// SubmitLimit must NOT call BookTicker, so the only route we register
// is /api/v3/order. Any access to /ticker/bookTicker fails the test.
type orderLimitOnly struct {
	orderCalls atomic.Int32
	order      http.HandlerFunc
	t          *testing.T
}

func (h *orderLimitOnly) handle(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/api/v3/ticker/bookTicker":
		h.t.Errorf("SubmitLimit must NOT call BookTicker (protocol §5.10 fixes MarketRef = limit_price)")
		w.WriteHeader(500)
	case "/api/v3/order":
		h.orderCalls.Add(1)
		h.order(w, r)
	default:
		http.NotFound(w, r)
	}
}

func newLimitOnlyClient(t *testing.T, h *orderLimitOnly) (*Client, *httptest.Server) {
	t.Helper()
	h.t = t
	srv := httptest.NewServer(http.HandlerFunc(h.handle))
	t.Cleanup(srv.Close)
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL:    srv.URL,
		HTTPClient: srv.Client(),
		NowFn:      fixedNow,
	})
	return c, srv
}

func TestSubmitLimit_BuildsLimitOrderParams(t *testing.T) {
	var orderQuery string
	h := &orderLimitOnly{
		order: func(w http.ResponseWriter, r *http.Request) {
			orderQuery = r.URL.RawQuery
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"orderId":100,
				"transactTime":1714000000999,
				"status":"NEW",
				"fills":[]
			}`))
		},
	}
	c, _ := newLimitOnlyClient(t, h)
	if _, err := c.SubmitLimit(context.Background(), fxLimitOrder()); err != nil {
		t.Fatalf("SubmitLimit: %v", err)
	}
	parsed, err := url.ParseQuery(orderQuery)
	if err != nil {
		t.Fatalf("ParseQuery: %v", err)
	}
	if got := parsed.Get("type"); got != "LIMIT" {
		t.Errorf("type = %q, want LIMIT", got)
	}
	if got := parsed.Get("timeInForce"); got != "GTC" {
		t.Errorf("timeInForce = %q, want GTC", got)
	}
	if got := parsed.Get("price"); got != "45000" {
		t.Errorf("price = %q, want 45000 (decimal.String of 45000.00 trims trailing zeros)", got)
	}
	if got := parsed.Get("symbol"); got != "BTCUSDT" {
		t.Errorf("symbol = %q", got)
	}
	if got := parsed.Get("side"); got != "BUY" {
		t.Errorf("side = %q, want BUY", got)
	}
	if got := parsed.Get("newOrderRespType"); got != "FULL" {
		t.Errorf("newOrderRespType = %q, want FULL", got)
	}
	if got := parsed.Get("newClientOrderId"); got != "01HKLIM000000000000000099" {
		t.Errorf("newClientOrderId = %q", got)
	}
}

func TestSubmitLimit_NewStatusEmptyFills(t *testing.T) {
	h := &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"orderId":101,
				"transactTime":1714000000111,
				"status":"NEW",
				"fills":[]
			}`))
		},
	}
	c, _ := newLimitOnlyClient(t, h)
	res, err := c.SubmitLimit(context.Background(), fxLimitOrder())
	if err != nil {
		t.Fatalf("SubmitLimit: %v", err)
	}
	if res.ExchangeOrderID != "101" {
		t.Errorf("ExchangeOrderID = %q, want 101", res.ExchangeOrderID)
	}
	if res.AcceptedAtMs != 1714000000111 {
		t.Errorf("AcceptedAtMs = %d", res.AcceptedAtMs)
	}
	if !res.MarketRef.Equal(decimal.RequireFromString("45000")) {
		t.Errorf("MarketRef = %s, want 45000 (limit_price)", res.MarketRef)
	}
	if len(res.Fills) != 0 {
		t.Errorf("len(Fills) = %d, want 0 (NEW status)", len(res.Fills))
	}
	if h.orderCalls.Load() != 1 {
		t.Errorf("orderCalls = %d, want 1", h.orderCalls.Load())
	}
}

func TestSubmitLimit_ImmediateFillCarriesFills(t *testing.T) {
	// Limit order that crosses the book — Binance returns FILLED inline.
	h := &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{
				"orderId":102,
				"transactTime":1714000000222,
				"status":"FILLED",
				"fills":[
					{"price":"44995.00","qty":"0.001","commission":"0.04499500","commissionAsset":"USDT"}
				]
			}`))
		},
	}
	c, _ := newLimitOnlyClient(t, h)
	res, err := c.SubmitLimit(context.Background(), fxLimitOrder())
	if err != nil {
		t.Fatalf("SubmitLimit: %v", err)
	}
	if len(res.Fills) != 1 {
		t.Fatalf("len(Fills) = %d, want 1", len(res.Fills))
	}
	if !res.Fills[0].FillPrice.Equal(decimal.RequireFromString("44995.00")) {
		t.Errorf("FillPrice = %s", res.Fills[0].FillPrice)
	}
	if !res.MarketRef.Equal(decimal.RequireFromString("45000")) {
		t.Errorf("MarketRef = %s, want 45000 (limit_price; protocol §5.10)", res.MarketRef)
	}
}

func TestSubmitLimit_ValidationErrors(t *testing.T) {
	c, _ := newLimitOnlyClient(t, &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			t.Error("/api/v3/order must not be reached when local validation fails")
			w.WriteHeader(500)
		},
	})
	bad := []struct {
		name  string
		mut   func(o *agent.ExchangeOrder)
		match string
	}{
		{"empty symbol", func(o *agent.ExchangeOrder) { o.Symbol = "" }, "empty symbol"},
		{"empty client_order_id", func(o *agent.ExchangeOrder) { o.ClientOrderID = "" }, "empty client_order_id"},
		{"zero quantity", func(o *agent.ExchangeOrder) { o.Quantity = decimal.Zero }, "quantity="},
		{"negative quantity", func(o *agent.ExchangeOrder) { o.Quantity = decimal.RequireFromString("-1") }, "must be positive"},
		{"zero limit_price", func(o *agent.ExchangeOrder) { o.LimitPrice = decimal.Zero }, "limit_price="},
		{"negative limit_price", func(o *agent.ExchangeOrder) { o.LimitPrice = decimal.RequireFromString("-1") }, "must be positive"},
		{"invalid side", func(o *agent.ExchangeOrder) { o.Side = "long" }, "invalid side"},
		{"wrong order_type", func(o *agent.ExchangeOrder) { o.OrderType = "market" }, "order_type="},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			o := fxLimitOrder()
			tc.mut(&o)
			_, err := c.SubmitLimit(context.Background(), o)
			if err == nil {
				t.Fatal("want error")
			}
			if !strings.Contains(err.Error(), tc.match) {
				t.Errorf("err = %v, want substring %q", err, tc.match)
			}
		})
	}
}

func TestSubmitLimit_BinanceAPIError_WrapsErrExchangeRejected(t *testing.T) {
	c, _ := newLimitOnlyClient(t, &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte(`{"code":-1013,"msg":"Filter failure: PRICE_FILTER"}`))
		},
	})
	_, err := c.SubmitLimit(context.Background(), fxLimitOrder())
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("err = %v, want errors.Is ErrExchangeRejected", err)
	}
	if !strings.Contains(err.Error(), "-1013") {
		t.Errorf("err = %v, want code -1013 in message", err)
	}
}

func TestSubmitLimit_RateLimit429WrapsErrExchangeRejectedWithReason(t *testing.T) {
	c, _ := newLimitOnlyClient(t, &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Retry-After", "20")
			w.WriteHeader(429)
			_, _ = w.Write([]byte(`{"code":-1003,"msg":"Too many requests."}`))
		},
	})
	_, err := c.SubmitLimit(context.Background(), fxLimitOrder())
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("err = %v, want errors.Is ErrExchangeRejected", err)
	}
	if !strings.Contains(err.Error(), "rate_limited") {
		t.Errorf("err = %v, want reason 'rate_limited'", err)
	}
	if !strings.Contains(err.Error(), "20s") {
		t.Errorf("err = %v, want retry_after=20s", err)
	}
}

func TestSubmitLimit_TransportErrorNotWrapped(t *testing.T) {
	// 5xx body with no Binance code → generic error, not ErrExchangeRejected.
	c, _ := newLimitOnlyClient(t, &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(502)
			_, _ = w.Write([]byte("<html>Bad Gateway</html>"))
		},
	})
	_, err := c.SubmitLimit(context.Background(), fxLimitOrder())
	if errors.Is(err, agent.ErrExchangeRejected) {
		t.Errorf("502 transport failure should NOT wrap ErrExchangeRejected; err = %v", err)
	}
}

func TestSubmitLimit_MissingOrderID(t *testing.T) {
	c, _ := newLimitOnlyClient(t, &orderLimitOnly{
		order: func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(200)
			_, _ = w.Write([]byte(`{"orderId":0,"transactTime":1,"fills":[]}`))
		},
	})
	_, err := c.SubmitLimit(context.Background(), fxLimitOrder())
	if err == nil || !strings.Contains(err.Error(), "missing orderId") {
		t.Errorf("err = %v, want 'missing orderId'", err)
	}
}
