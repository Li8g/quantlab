package binance

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"quantlab/internal/agent"
	"quantlab/internal/wire"
)

// ===== decoder unit tests =====

func TestDecodeExecutionReport_TRADE_PartialFilled(t *testing.T) {
	frame := []byte(`{
		"e":"executionReport",
		"s":"BTCUSDT",
		"c":"COID-001",
		"S":"BUY",
		"o":"LIMIT",
		"x":"TRADE",
		"X":"PARTIALLY_FILLED",
		"i":424242,
		"l":"0.0005",
		"L":"49995.50",
		"n":"0.00250",
		"N":"USDT",
		"T":1714000001000,
		"z":"0.0005"
	}`)
	ev, ok, err := decodeExecutionReport(frame)
	if err != nil {
		t.Fatalf("decodeExecutionReport: %v", err)
	}
	if !ok {
		t.Fatal("ok=false, want surfaced event")
	}
	if ev.ClientOrderID != "COID-001" {
		t.Errorf("ClientOrderID = %q", ev.ClientOrderID)
	}
	if ev.ExchangeOrderID != "424242" {
		t.Errorf("ExchangeOrderID = %q, want 424242", ev.ExchangeOrderID)
	}
	if ev.Status != wire.OrderStatusPartialFilled {
		t.Errorf("Status = %q, want partial_filled", ev.Status)
	}
	if ev.Side != "buy" {
		t.Errorf("Side = %q, want lowercase 'buy'", ev.Side)
	}
	if ev.Fill == nil {
		t.Fatal("Fill = nil, want populated for TRADE")
	}
	if ev.Fill.FillPrice.String() != "49995.5" {
		t.Errorf("FillPrice = %s", ev.Fill.FillPrice)
	}
	if ev.Fill.FillQuantity.String() != "0.0005" {
		t.Errorf("FillQuantity = %s", ev.Fill.FillQuantity)
	}
	if ev.Fill.FillFeeAsset != "USDT" {
		t.Errorf("FillFeeAsset = %q", ev.Fill.FillFeeAsset)
	}
	if ev.Fill.FilledAtExchangeMs != 1714000001000 {
		t.Errorf("FilledAtExchangeMs = %d", ev.Fill.FilledAtExchangeMs)
	}
	if ev.CumulativeFillQuantity.String() != "0.0005" {
		t.Errorf("CumulativeFillQuantity = %s, want 0.0005", ev.CumulativeFillQuantity)
	}
}

func TestDecodeExecutionReport_TRADE_Filled(t *testing.T) {
	frame := []byte(`{
		"e":"executionReport","c":"COID-2","S":"SELL","x":"TRADE","X":"FILLED",
		"i":1,"l":"1","L":"50000","n":"0.5","N":"USDT","T":1,"z":"1"
	}`)
	ev, ok, err := decodeExecutionReport(frame)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if ev.Status != wire.OrderStatusFilled {
		t.Errorf("Status = %q, want filled", ev.Status)
	}
	if ev.Side != "sell" {
		t.Errorf("Side = %q, want 'sell'", ev.Side)
	}
}

func TestDecodeExecutionReport_CANCELED(t *testing.T) {
	frame := []byte(`{"e":"executionReport","c":"COID-3","x":"CANCELED","X":"CANCELED","i":1,"S":"BUY"}`)
	ev, ok, err := decodeExecutionReport(frame)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if ev.Status != wire.OrderStatusCancelled {
		t.Errorf("Status = %q, want cancelled", ev.Status)
	}
	if ev.Fill != nil {
		t.Errorf("Fill = %+v, want nil for CANCELED", ev.Fill)
	}
}

func TestDecodeExecutionReport_REJECTED(t *testing.T) {
	frame := []byte(`{"e":"executionReport","c":"COID-4","x":"REJECTED","X":"REJECTED","i":1,"S":"BUY"}`)
	ev, ok, err := decodeExecutionReport(frame)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if ev.Status != wire.OrderStatusRejected {
		t.Errorf("Status = %q, want rejected", ev.Status)
	}
}

func TestDecodeExecutionReport_EXPIRED_MapsToCancelled(t *testing.T) {
	// Binance EXPIRED → wire cancelled (SaaS doesn't distinguish v1).
	frame := []byte(`{"e":"executionReport","c":"COID-5","x":"EXPIRED","X":"EXPIRED","i":1,"S":"BUY"}`)
	ev, ok, err := decodeExecutionReport(frame)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if ev.Status != wire.OrderStatusCancelled {
		t.Errorf("Status = %q, want cancelled", ev.Status)
	}
}

func TestDecodeExecutionReport_NEW_Dropped(t *testing.T) {
	frame := []byte(`{"e":"executionReport","c":"COID-6","x":"NEW","X":"NEW","i":1,"S":"BUY"}`)
	_, ok, err := decodeExecutionReport(frame)
	if err != nil {
		t.Fatalf("err = %v", err)
	}
	if ok {
		t.Error("ok=true, want false (NEW must be dropped — synchronous Ack covers it)")
	}
}

func TestDecodeExecutionReport_REPLACED_Dropped(t *testing.T) {
	frame := []byte(`{"e":"executionReport","c":"COID-7","x":"REPLACED","X":"NEW","i":1,"S":"BUY"}`)
	_, ok, _ := decodeExecutionReport(frame)
	if ok {
		t.Error("ok=true, want false (REPLACED out of v1 scope)")
	}
}

func TestDecodeExecutionReport_MissingClientOrderID(t *testing.T) {
	frame := []byte(`{"e":"executionReport","x":"TRADE","X":"FILLED","i":1,"S":"BUY","l":"1","L":"1","n":"0","N":"U","T":1,"z":"1"}`)
	_, _, err := decodeExecutionReport(frame)
	if err == nil || !strings.Contains(err.Error(), "client_order_id") {
		t.Errorf("err = %v, want 'client_order_id' missing", err)
	}
}

func TestDecodeExecutionReport_BadJSON(t *testing.T) {
	_, _, err := decodeExecutionReport([]byte(`not json`))
	if err == nil {
		t.Fatal("want decode error")
	}
}

func TestDecodeExecutionReport_InvalidFillQty(t *testing.T) {
	frame := []byte(`{"e":"executionReport","c":"COID-8","x":"TRADE","X":"FILLED","i":1,"S":"BUY","l":"NaN","L":"1","n":"0","N":"U","T":1,"z":"1"}`)
	_, _, err := decodeExecutionReport(frame)
	if err == nil || !strings.Contains(err.Error(), "LastFillQty") {
		t.Errorf("err = %v, want LastFillQty parse failure", err)
	}
}

func TestDecodeExecutionReport_EmptyCommissionIsZero(t *testing.T) {
	frame := []byte(`{"e":"executionReport","c":"COID-9","x":"TRADE","X":"FILLED","i":1,"S":"BUY","l":"1","L":"1","n":"","N":"","T":1,"z":"1"}`)
	ev, ok, err := decodeExecutionReport(frame)
	if err != nil || !ok {
		t.Fatalf("err=%v ok=%v", err, ok)
	}
	if !ev.Fill.FillFeeAmount.IsZero() {
		t.Errorf("FillFeeAmount = %s, want 0 for empty commission", ev.Fill.FillFeeAmount)
	}
}

// ===== streamBaseURL derivation =====

func TestDefaultStreamBaseURL(t *testing.T) {
	cases := []struct {
		rest, want string
	}{
		{BaseURLMainnet, StreamBaseURLMainnet},
		{BaseURLTestnet, StreamBaseURLTestnet},
		{"", StreamBaseURLMainnet}, // empty REST → fall back to mainnet stream
		{"http://my-mirror.example", ""},
	}
	for _, tc := range cases {
		if got := defaultStreamBaseURL(tc.rest); got != tc.want {
			t.Errorf("defaultStreamBaseURL(%q) = %q, want %q", tc.rest, got, tc.want)
		}
	}
}

// ===== Subscribe + dispatch =====

func TestUDS_Subscribe_LastWinsReplacement(t *testing.T) {
	c := NewClient("k", "s", Options{})
	u := newUDS(c, udsOptions{StreamBaseURL: "ws://test"})
	var firstHits, secondHits atomic.Int32
	u.Subscribe(func(agent.OrderEvent) { firstHits.Add(1) })
	u.Subscribe(func(agent.OrderEvent) { secondHits.Add(1) })
	u.dispatch(agent.OrderEvent{ClientOrderID: "x"})
	if firstHits.Load() != 0 {
		t.Errorf("first cb hits = %d, want 0 (replaced)", firstHits.Load())
	}
	if secondHits.Load() != 1 {
		t.Errorf("second cb hits = %d, want 1", secondHits.Load())
	}
}

func TestUDS_Dispatch_NilCallbackNoop(t *testing.T) {
	c := NewClient("k", "s", Options{})
	u := newUDS(c, udsOptions{StreamBaseURL: "ws://test"})
	// No Subscribe — must not panic.
	u.dispatch(agent.OrderEvent{ClientOrderID: "x"})
}

// ===== WS integration test =====

// wsTestServer wraps an httptest.Server that serves both:
//   - /api/v3/userDataStream  (REST listenKey lifecycle)
//   - /ws/<listenKey>         (WebSocket frames)
//
// The frames channel lets the test push synthetic Binance events; the
// closed channel signals server-initiated disconnect.
type wsTestServer struct {
	srv         *httptest.Server
	frames      chan string
	disconnect  chan struct{}
	listenKey   string
	createCount atomic.Int32
	putCount    atomic.Int32

	connWG sync.WaitGroup
}

func newWSTestServer(t *testing.T, listenKey string) *wsTestServer {
	t.Helper()
	w := &wsTestServer{
		frames:     make(chan string, 16),
		disconnect: make(chan struct{}),
		listenKey:  listenKey,
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v3/userDataStream", func(rw http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			w.createCount.Add(1)
			rw.Header().Set("Content-Type", "application/json")
			_, _ = rw.Write([]byte(`{"listenKey":"` + listenKey + `"}`))
		case http.MethodPut:
			w.putCount.Add(1)
			_, _ = rw.Write([]byte(`{}`))
		case http.MethodDelete:
			_, _ = rw.Write([]byte(`{}`))
		}
	})
	mux.HandleFunc("/ws/"+listenKey, func(rw http.ResponseWriter, r *http.Request) {
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := up.Upgrade(rw, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		w.connWG.Add(1)
		defer w.connWG.Done()
		defer conn.Close()
		for {
			select {
			case frame := <-w.frames:
				if err := conn.WriteMessage(websocket.TextMessage, []byte(frame)); err != nil {
					return
				}
			case <-w.disconnect:
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	w.srv = srv
	t.Cleanup(func() {
		close(w.disconnect)
		srv.Close()
		w.connWG.Wait()
	})
	return w
}

func (w *wsTestServer) restURL() string { return w.srv.URL }
func (w *wsTestServer) wsBaseURL() string {
	return "ws" + strings.TrimPrefix(w.srv.URL, "http")
}

func TestUDS_EndToEnd_ExecutionReportReachesCallback(t *testing.T) {
	srv := newWSTestServer(t, "lk-001")
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL:    srv.restURL(),
		HTTPClient: srv.srv.Client(),
	})
	u := newUDS(c, udsOptions{
		StreamBaseURL: srv.wsBaseURL(),
		ReconnectMin:  10 * time.Millisecond,
		ReconnectMax:  20 * time.Millisecond,
	})
	events := make(chan agent.OrderEvent, 4)
	u.Subscribe(func(ev agent.OrderEvent) { events <- ev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	t.Cleanup(func() { u.Close(); u.Wait() })

	// Wait for the server to record a successful listenKey creation
	// (means dial + REST round-trip done).
	if !waitInt32(&srv.createCount, 1, 1*time.Second) {
		t.Fatal("CreateListenKey never observed")
	}

	srv.frames <- `{
		"e":"executionReport","c":"COID-RT","S":"BUY","o":"LIMIT",
		"x":"TRADE","X":"FILLED","i":777,"l":"0.001","L":"45100.00",
		"n":"0.045","N":"USDT","T":1714000099000,"z":"0.001"
	}`

	select {
	case ev := <-events:
		if ev.ClientOrderID != "COID-RT" {
			t.Errorf("ClientOrderID = %q", ev.ClientOrderID)
		}
		if ev.Status != wire.OrderStatusFilled {
			t.Errorf("Status = %q, want filled", ev.Status)
		}
		if ev.ExchangeOrderID != "777" {
			t.Errorf("ExchangeOrderID = %q", ev.ExchangeOrderID)
		}
		if ev.Fill == nil || ev.Fill.FillQuantity.String() != "0.001" {
			t.Errorf("Fill = %+v", ev.Fill)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("event never reached callback")
	}
}

func TestUDS_EndToEnd_UnknownEventDoesNotKillStream(t *testing.T) {
	srv := newWSTestServer(t, "lk-002")
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL: srv.restURL(), HTTPClient: srv.srv.Client(),
	})
	u := newUDS(c, udsOptions{
		StreamBaseURL: srv.wsBaseURL(),
		ReconnectMin:  10 * time.Millisecond,
		ReconnectMax:  20 * time.Millisecond,
	})
	events := make(chan agent.OrderEvent, 4)
	u.Subscribe(func(ev agent.OrderEvent) { events <- ev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	t.Cleanup(func() { u.Close(); u.Wait() })

	waitInt32(&srv.createCount, 1, 1*time.Second)
	// Unknown event type — must be ignored.
	srv.frames <- `{"e":"outboundAccountPosition","a":[]}`
	// Then a real event — verifies the read loop kept running.
	srv.frames <- `{"e":"executionReport","c":"COID-AFTER","S":"BUY","x":"TRADE","X":"FILLED","i":1,"l":"1","L":"1","n":"0","N":"U","T":1,"z":"1"}`

	select {
	case ev := <-events:
		if ev.ClientOrderID != "COID-AFTER" {
			t.Errorf("got ClientOrderID = %q", ev.ClientOrderID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("subsequent event never reached callback (read loop died?)")
	}
}

func TestUDS_EndToEnd_ReconnectsAfterDisconnect(t *testing.T) {
	srv := newWSTestServer(t, "lk-003")
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL: srv.restURL(), HTTPClient: srv.srv.Client(),
	})
	u := newUDS(c, udsOptions{
		StreamBaseURL: srv.wsBaseURL(),
		ReconnectMin:  10 * time.Millisecond,
		ReconnectMax:  30 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	t.Cleanup(func() { u.Close(); u.Wait() })

	// First connection.
	if !waitInt32(&srv.createCount, 1, 1*time.Second) {
		t.Fatal("first CreateListenKey never observed")
	}
	// Simulate server-side disconnect: close the existing WS by
	// switching to a new server. Instead — issue a close from server
	// side. The simplest way: re-open a fresh server (skip).
	// Reuse: signal the active conn to close by pushing a corrupt
	// frame and then closing via disconnect chan would interrupt the
	// frame channel for the next conn. We need to close just THIS
	// session.
	//
	// Easier: push a listenKeyExpired event — the read loop returns
	// errListenKeyExpired which triggers the reconnect path.
	srv.frames <- `{"e":"listenKeyExpired"}`

	// Wait for a SECOND CreateListenKey call (proof of reconnect).
	if !waitInt32(&srv.createCount, 2, 2*time.Second) {
		t.Fatalf("second CreateListenKey never observed; got %d", srv.createCount.Load())
	}
}

func TestUDS_Close_StopsRun(t *testing.T) {
	srv := newWSTestServer(t, "lk-004")
	c := NewClient("PUBKEY", "SECRET", Options{
		BaseURL: srv.restURL(), HTTPClient: srv.srv.Client(),
	})
	u := newUDS(c, udsOptions{
		StreamBaseURL: srv.wsBaseURL(),
		ReconnectMin:  10 * time.Millisecond,
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u.Start(ctx)
	waitInt32(&srv.createCount, 1, 1*time.Second)
	u.Close()

	doneCh := make(chan struct{})
	go func() { u.Wait(); close(doneCh) }()
	select {
	case <-doneCh:
	case <-time.After(2 * time.Second):
		t.Fatal("Close+Wait did not return; runtime goroutine stuck")
	}
}

func TestUDS_DoubleCloseIdempotent(t *testing.T) {
	c := NewClient("k", "s", Options{})
	u := newUDS(c, udsOptions{StreamBaseURL: "ws://test"})
	u.Close()
	u.Close() // panic on double stopCh close would fail here
	u.Close()
}

// ===== exchange-level integration =====

func TestExchange_OrderEventStreamerInterfaceWired(t *testing.T) {
	// Compile-time assertion already in exchange.go; this test
	// surfaces a regression next to behavior instead of only at the
	// package declaration site.
	var _ agent.OrderEventStreamer = NewExchange("k", "s", ExchangeOptions{})
}

func TestExchange_UDSDisabled_SubscribeStillSafe(t *testing.T) {
	ex := NewExchange("k", "s", ExchangeOptions{UDSDisabled: true})
	if ex.uds != nil {
		t.Error("UDSDisabled: uds should be nil")
	}
	hit := 0
	ex.Subscribe(func(agent.OrderEvent) { hit++ })
	// No goroutine, no event source — Subscribe is a no-op functionally.
	if hit != 0 {
		t.Errorf("hit = %d, want 0", hit)
	}
	// Start + Close on a UDS-less Exchange must still be safe.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)
	_ = ex.Close()
}

func TestExchange_SubscribeBeforeStartReplayedOnStart(t *testing.T) {
	// Subscribe lands while uds.run is not yet active; Start must
	// re-Subscribe so the first event isn't dropped.
	srv := newWSTestServer(t, "lk-pre")
	ex := NewExchange("PUBKEY", "SECRET", ExchangeOptions{
		BaseURL:         srv.restURL(),
		HTTPClient:      srv.srv.Client(),
		StreamBaseURL:   srv.wsBaseURL(),
		PingInterval:    10 * time.Hour,
		UDSReconnectMin: 10 * time.Millisecond,
	})
	events := make(chan agent.OrderEvent, 1)
	ex.Subscribe(func(ev agent.OrderEvent) { events <- ev })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ex.Start(ctx)
	t.Cleanup(func() { _ = ex.Close() })

	if !waitInt32(&srv.createCount, 1, 1*time.Second) {
		t.Fatal("CreateListenKey never observed")
	}
	srv.frames <- `{"e":"executionReport","c":"COID-PRE","S":"BUY","x":"TRADE","X":"FILLED","i":1,"l":"1","L":"1","n":"0","N":"U","T":1,"z":"1"}`

	select {
	case ev := <-events:
		if ev.ClientOrderID != "COID-PRE" {
			t.Errorf("ClientOrderID = %q", ev.ClientOrderID)
		}
	case <-time.After(1 * time.Second):
		t.Fatal("event from pre-Start Subscribe not delivered")
	}
}

// ===== helpers =====

// waitInt32 spins until counter ≥ n or deadline expires. Returns true
// when the threshold was reached.
func waitInt32(c *atomic.Int32, n int32, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if c.Load() >= n {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return false
}
