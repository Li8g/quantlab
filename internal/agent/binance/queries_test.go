package binance

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

// =================== Ping ===================

func TestPing_HappyPath(t *testing.T) {
	var gotPath, gotMethod, gotAuth string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotMethod = r.Method
		gotAuth = r.Header.Get("X-MBX-APIKEY")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{}`))
	})
	if err := c.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	if gotPath != "/api/v3/ping" {
		t.Errorf("path = %q", gotPath)
	}
	if gotMethod != http.MethodGet {
		t.Errorf("method = %q", gotMethod)
	}
	if gotAuth != "" {
		t.Errorf("Ping should be unsigned, but X-MBX-APIKEY = %q", gotAuth)
	}
}

func TestPing_PropagatesAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(418) // Binance returns 418 when banned
		_, _ = w.Write([]byte(`{"code":-1003,"msg":"Way too much request weight used."}`))
	})
	err := c.Ping(context.Background())
	if err == nil {
		t.Fatal("want error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != -1003 {
		t.Errorf("Code = %d, want -1003", apiErr.Code)
	}
}

// =================== BookTicker ===================

func TestBookTicker_HappyPath(t *testing.T) {
	var gotQuery, gotAuth string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		gotAuth = r.Header.Get("X-MBX-APIKEY")
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"symbol": "BTCUSDT",
			"bidPrice": "50000.10",
			"bidQty": "1.5",
			"askPrice": "50001.20",
			"askQty": "2.0"
		}`))
	})
	bt, err := c.BookTicker(context.Background(), "BTCUSDT")
	if err != nil {
		t.Fatalf("BookTicker: %v", err)
	}
	if gotQuery != "symbol=BTCUSDT" {
		t.Errorf("query = %q", gotQuery)
	}
	if gotAuth != "" {
		t.Errorf("BookTicker should be unsigned, but X-MBX-APIKEY = %q", gotAuth)
	}
	if bt.Symbol != "BTCUSDT" {
		t.Errorf("Symbol = %q", bt.Symbol)
	}
	if !bt.BidPrice.Equal(decimal.RequireFromString("50000.10")) {
		t.Errorf("BidPrice = %s, want 50000.10", bt.BidPrice)
	}
	if !bt.AskPrice.Equal(decimal.RequireFromString("50001.20")) {
		t.Errorf("AskPrice = %s, want 50001.20", bt.AskPrice)
	}
	if !bt.BidQty.Equal(decimal.RequireFromString("1.5")) {
		t.Errorf("BidQty = %s", bt.BidQty)
	}
	if !bt.AskQty.Equal(decimal.RequireFromString("2.0")) {
		t.Errorf("AskQty = %s", bt.AskQty)
	}
}

func TestBookTicker_RejectsEmptySymbol(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("server should not be hit for empty symbol")
		w.WriteHeader(200)
	})
	_, err := c.BookTicker(context.Background(), "")
	if err == nil {
		t.Fatal("want error on empty symbol")
	}
	if !strings.Contains(err.Error(), "empty symbol") {
		t.Errorf("err = %v, want 'empty symbol'", err)
	}
}

func TestBookTicker_MalformedDecimal(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"symbol": "BTCUSDT",
			"bidPrice": "not-a-number",
			"bidQty": "1.5",
			"askPrice": "50001.20",
			"askQty": "2.0"
		}`))
	})
	_, err := c.BookTicker(context.Background(), "BTCUSDT")
	if err == nil {
		t.Fatal("want error on malformed bidPrice")
	}
	if !strings.Contains(err.Error(), "bidPrice") {
		t.Errorf("err = %v, want mention of bidPrice", err)
	}
}

func TestBookTicker_PropagatesAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`{"code":-1121,"msg":"Invalid symbol."}`))
	})
	_, err := c.BookTicker(context.Background(), "BOGUS")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != -1121 {
		t.Errorf("Code = %d, want -1121", apiErr.Code)
	}
}

// =================== Account ===================

func TestAccount_HappyPathFiltersZeroBalances(t *testing.T) {
	var gotAuth, gotQuery string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("X-MBX-APIKEY")
		gotQuery = r.URL.RawQuery
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"makerCommission": 15,
			"balances": [
				{"asset": "BTC",  "free": "1.5",      "locked": "0.3"},
				{"asset": "USDT", "free": "10000.00", "locked": "0"},
				{"asset": "BNB",  "free": "0",        "locked": "0"},
				{"asset": "ETH",  "free": "0",        "locked": "0.50"}
			]
		}`))
	})
	pos, err := c.Account(context.Background())
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if gotAuth != "PUBKEY" {
		t.Errorf("X-MBX-APIKEY = %q, want PUBKEY (Account must be signed)", gotAuth)
	}
	// Verify signed: timestamp + signature in query.
	for _, key := range []string{"timestamp=", "signature="} {
		if !strings.Contains(gotQuery, key) {
			t.Errorf("query missing %s: %s", key, gotQuery)
		}
	}

	// Expect BTC, USDT, ETH; BNB filtered (all-zero).
	if len(pos) != 3 {
		t.Fatalf("len(pos) = %d, want 3 (BNB should be filtered)", len(pos))
	}
	byAsset := map[string]agent.Position{}
	for _, p := range pos {
		byAsset[p.Symbol] = p
	}
	btc, ok := byAsset["BTC"]
	if !ok {
		t.Fatal("BTC missing")
	}
	if !btc.Free.Equal(decimal.RequireFromString("1.5")) {
		t.Errorf("BTC.Free = %s", btc.Free)
	}
	if !btc.Locked.Equal(decimal.RequireFromString("0.3")) {
		t.Errorf("BTC.Locked = %s", btc.Locked)
	}
	usdt, ok := byAsset["USDT"]
	if !ok {
		t.Fatal("USDT missing")
	}
	if !usdt.Free.Equal(decimal.RequireFromString("10000.00")) {
		t.Errorf("USDT.Free = %s", usdt.Free)
	}
	eth, ok := byAsset["ETH"]
	if !ok {
		t.Fatal("ETH missing (locked-only must not be filtered)")
	}
	if !eth.Locked.Equal(decimal.RequireFromString("0.50")) {
		t.Errorf("ETH.Locked = %s", eth.Locked)
	}
	if _, hasBNB := byAsset["BNB"]; hasBNB {
		t.Error("BNB (all-zero) should have been filtered")
	}
}

func TestAccount_EmptyBalancesReturnsEmptySlice(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{"balances": []}`))
	})
	pos, err := c.Account(context.Background())
	if err != nil {
		t.Fatalf("Account: %v", err)
	}
	if len(pos) != 0 {
		t.Errorf("len(pos) = %d, want 0", len(pos))
	}
}

func TestAccount_MalformedDecimal(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(200)
		_, _ = w.Write([]byte(`{
			"balances": [
				{"asset": "BTC", "free": "garbage", "locked": "0"}
			]
		}`))
	})
	_, err := c.Account(context.Background())
	if err == nil {
		t.Fatal("want error on malformed free")
	}
	if !strings.Contains(err.Error(), "free") || !strings.Contains(err.Error(), "BTC") {
		t.Errorf("err = %v, want mention of free + BTC", err)
	}
}

func TestAccount_PropagatesAPIError(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(401)
		_, _ = w.Write([]byte(`{"code":-2014,"msg":"API-key format invalid."}`))
	})
	_, err := c.Account(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Code != -2014 {
		t.Errorf("Code = %d, want -2014", apiErr.Code)
	}
}

func TestAccount_RejectsEmptyAPIKey(t *testing.T) {
	c := NewClient("", "SECRET", Options{
		BaseURL: "http://unreachable",
		NowFn:   fixedNow,
	})
	_, err := c.Account(context.Background())
	if !errors.Is(err, ErrEmptyAPIKey) {
		t.Errorf("err = %v, want ErrEmptyAPIKey", err)
	}
}
