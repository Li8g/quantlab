package binance

import (
	"errors"
	"testing"

	"github.com/shopspring/decimal"

	"quantlab/internal/agent"
)

func dec(t *testing.T, s string) decimal.Decimal {
	t.Helper()
	d, err := decimal.NewFromString(s)
	if err != nil {
		t.Fatalf("decimal %q: %v", s, err)
	}
	return d
}

// btcFilter mirrors the real BTCUSDT testnet filters that surfaced the
// -1013 LOT_SIZE rejection: step/min 0.00001, tick 0.01, minNotional 5.
func btcFilter(t *testing.T) *symbolFilter {
	return &symbolFilter{
		StepSize:    dec(t, "0.00001"),
		MinQty:      dec(t, "0.00001"),
		TickSize:    dec(t, "0.01"),
		MinNotional: dec(t, "5"),
	}
}

func TestParseSymbolFilter_AllFilters(t *testing.T) {
	body := []byte(`{"symbols":[{"symbol":"BTCUSDT","filters":[
		{"filterType":"PRICE_FILTER","tickSize":"0.01000000","minPrice":"0.01"},
		{"filterType":"LOT_SIZE","stepSize":"0.00001000","minQty":"0.00001000","maxQty":"9000"},
		{"filterType":"NOTIONAL","minNotional":"5.00000000"}
	]}]}`)
	f, err := parseSymbolFilter(body)
	if err != nil {
		t.Fatalf("parseSymbolFilter: %v", err)
	}
	if !f.StepSize.Equal(dec(t, "0.00001")) {
		t.Errorf("StepSize = %s, want 0.00001", f.StepSize)
	}
	if !f.MinQty.Equal(dec(t, "0.00001")) {
		t.Errorf("MinQty = %s, want 0.00001", f.MinQty)
	}
	if !f.TickSize.Equal(dec(t, "0.01")) {
		t.Errorf("TickSize = %s, want 0.01", f.TickSize)
	}
	if !f.MinNotional.Equal(dec(t, "5")) {
		t.Errorf("MinNotional = %s, want 5", f.MinNotional)
	}
}

// The legacy MIN_NOTIONAL filter populates the same floor as NOTIONAL.
func TestParseSymbolFilter_LegacyMinNotional(t *testing.T) {
	body := []byte(`{"symbols":[{"symbol":"ETHUSDT","filters":[
		{"filterType":"LOT_SIZE","stepSize":"0.0001","minQty":"0.0001"},
		{"filterType":"MIN_NOTIONAL","minNotional":"10.0"}
	]}]}`)
	f, err := parseSymbolFilter(body)
	if err != nil {
		t.Fatalf("parseSymbolFilter: %v", err)
	}
	if !f.MinNotional.Equal(dec(t, "10")) {
		t.Errorf("MinNotional = %s, want 10", f.MinNotional)
	}
}

func TestParseSymbolFilter_NoSymbols(t *testing.T) {
	if _, err := parseSymbolFilter([]byte(`{"symbols":[]}`)); err == nil {
		t.Fatal("want error for empty symbols")
	}
}

func TestParseSymbolFilter_BadJSON(t *testing.T) {
	if _, err := parseSymbolFilter([]byte(`not json`)); err == nil {
		t.Fatal("want decode error")
	}
}

// TestCompliantQuantity_SnapsOffGridDown is the exact case that produced
// the -1013: a SaaS-rendered 8-decimal quantity floored to the 5-decimal
// LOT_SIZE grid.
func TestCompliantQuantity_SnapsOffGridDown(t *testing.T) {
	f := btcFilter(t)
	ref := dec(t, "66893.99")
	got, err := compliantQuantity(dec(t, "0.45264902"), ref, f)
	if err != nil {
		t.Fatalf("compliantQuantity: %v", err)
	}
	if !got.Equal(dec(t, "0.45264")) {
		t.Errorf("snapped qty = %s, want 0.45264", got)
	}
	// And the snapped value must render without spurious precision so
	// Binance accepts it verbatim.
	if got.String() != "0.45264" {
		t.Errorf("snapped qty string = %q, want \"0.45264\"", got.String())
	}
}

// An already-on-grid quantity is returned untouched (same representation),
// so compliant orders are a no-op.
func TestCompliantQuantity_OnGridUnchanged(t *testing.T) {
	f := btcFilter(t)
	in := dec(t, "0.001")
	got, err := compliantQuantity(in, dec(t, "50000"), f)
	if err != nil {
		t.Fatalf("compliantQuantity: %v", err)
	}
	if got.String() != "0.001" {
		t.Errorf("on-grid qty = %q, want \"0.001\" (unchanged)", got.String())
	}
}

func TestCompliantQuantity_BelowMinQtyRejected(t *testing.T) {
	f := btcFilter(t)
	// 0.000005 floors to 0 on a 0.00001 grid → floored-to-zero rejection.
	_, err := compliantQuantity(dec(t, "0.000005"), dec(t, "66000"), f)
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Fatalf("err = %v, want ErrExchangeRejected", err)
	}
}

func TestCompliantQuantity_BelowMinNotionalRejected(t *testing.T) {
	f := btcFilter(t)
	// 0.00002 BTC × 66000 = 1.32 USDT < 5 minNotional. Quantity itself
	// clears minQty and the grid, so this isolates the notional gate.
	_, err := compliantQuantity(dec(t, "0.00002"), dec(t, "66000"), f)
	if !errors.Is(err, agent.ErrExchangeRejected) {
		t.Fatalf("err = %v, want ErrExchangeRejected (notional 1.32 < 5)", err)
	}
}

// With no advertised filters, the quantity passes through and no floor is
// enforced (zero fields disable each check).
func TestCompliantQuantity_NoFiltersPassThrough(t *testing.T) {
	got, err := compliantQuantity(dec(t, "0.12345678"), dec(t, "100"), &symbolFilter{})
	if err != nil {
		t.Fatalf("compliantQuantity: %v", err)
	}
	if !got.Equal(dec(t, "0.12345678")) {
		t.Errorf("got %s, want unchanged 0.12345678", got)
	}
}

func TestCompliantPrice_SnapsToTick(t *testing.T) {
	f := btcFilter(t)
	got := compliantPrice(dec(t, "66893.997"), f) // tick 0.01 → nearest 66894.00
	if !got.Equal(dec(t, "66894")) {
		t.Errorf("snapped price = %s, want 66894", got)
	}
}

func TestCompliantPrice_OnGridUnchanged(t *testing.T) {
	f := btcFilter(t)
	in := dec(t, "45000.55") // on the 0.01 tick grid
	got := compliantPrice(in, f)
	if got.String() != "45000.55" {
		t.Errorf("on-grid price = %q, want \"45000.55\" (unchanged)", got.String())
	}
}
