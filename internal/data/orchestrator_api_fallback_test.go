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

// klineRowJSON formats one bar in Binance's /api/v3/klines 12-column shape
// (numeric openTime/closeTime/numTrades, string prices) so a test server
// can emit pages the real parser accepts.
func klineRowJSON(openTime int64) string {
	return fmt.Sprintf(
		`[%d,"100.0","101.0","99.0","100.5","1.0",%d,"100.0",10,"0.5","50.0","0"]`,
		openTime, openTime+59_999)
}

// TestFetchMonthViaAPI_PaginatesWholeMonth drives the REST fallback against
// an in-process server that pages 2 bars at a time. It asserts the
// orchestrator walks forward across pages, stops on the empty page, returns
// every January bar in ascending order with no duplicates, and excludes a
// bar dated exactly at the next month's first ms (the [first, last] inclusive
// month bound).
func TestFetchMonthViaAPI_PaginatesWholeMonth(t *testing.T) {
	const pageSize = 2
	jan := time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC)
	feb := time.Date(2025, 2, 1, 0, 0, 0, 0, time.UTC)

	janBars := []int64{
		jan.UnixMilli(),
		jan.Add(1 * time.Minute).UnixMilli(),
		jan.Add(2 * time.Minute).UnixMilli(),
		jan.Add(3 * time.Minute).UnixMilli(),
		jan.Add(4 * time.Minute).UnixMilli(),
	}
	// A bar at Feb-1 00:00 must NOT be returned: it belongs to the next month.
	allBars := append(append([]int64{}, janBars...), feb.UnixMilli())

	var calls atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		q := r.URL.Query()
		start, _ := strconv.ParseInt(q.Get("startTime"), 10, 64)
		end, _ := strconv.ParseInt(q.Get("endTime"), 10, 64)

		var page []string
		for _, ot := range allBars {
			if ot >= start && ot <= end {
				page = append(page, klineRowJSON(ot))
				if len(page) == pageSize {
					break
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, "[%s]", strings.Join(page, ","))
	}))
	defer srv.Close()

	c, _ := newTestClient(srv)
	o := &Orchestrator{API: c}

	rows, err := o.fetchMonthViaAPI(context.Background(), "BTCUSDT", "1m", 2025, 1)
	if err != nil {
		t.Fatalf("fetchMonthViaAPI: %v", err)
	}

	if len(rows) != len(janBars) {
		t.Fatalf("got %d rows, want %d (Feb-1 bar must be excluded)", len(rows), len(janBars))
	}
	for i, r := range rows {
		if r.OpenTime != janBars[i] {
			t.Errorf("row %d OpenTime = %d, want %d (ascending, no dupes)", i, r.OpenTime, janBars[i])
		}
	}
	// 5 bars at pageSize 2 → pages [0,1],[2,3],[4],[] = 4 calls; assert the
	// loop actually paginated rather than one-shotting.
	if n := calls.Load(); n < 3 {
		t.Errorf("server called %d times, want ≥3 (pagination should span multiple pages)", n)
	}
}

// TestFetchMonthViaAPI_NilAPIErrors confirms the fallback fails loudly
// rather than panicking when no API client is configured (Orchestrator.API
// nil disables the fallback; ImportSymbol only calls this when API != nil,
// but the guard keeps the method safe in isolation).
func TestFetchMonthViaAPI_NilAPIErrors(t *testing.T) {
	o := &Orchestrator{}
	_, err := o.fetchMonthViaAPI(context.Background(), "BTCUSDT", "1m", 2025, 1)
	if err == nil {
		t.Fatal("expected error when Orchestrator.API is nil")
	}
}
