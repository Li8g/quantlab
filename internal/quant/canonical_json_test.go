package quant

import (
	"testing"

	"quantlab/internal/domain"
)

// TestBarsHashExcludesMetadata is priority test #12.
// Changing Bar.IsGap or Bar.GapType must not alter bars_hash — gap-detection
// algorithm upgrades must not invalidate the reproducibility guarantee.
func TestBarsHashExcludesMetadata(t *testing.T) {
	base := []domain.Bar{
		{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 10.5, Volume: 100.0},
		{OpenTime: 2000, Open: 10.5, High: 12.0, Low: 10.0, Close: 11.0, Volume: 150.0},
	}
	withGap := make([]domain.Bar, len(base))
	copy(withGap, base)
	withGap[0].IsGap = true
	withGap[0].GapType = "price_gap"
	withGap[1].IsGap = true
	withGap[1].GapType = "volume_gap"

	h1, err := BarsHash(base)
	if err != nil {
		t.Fatalf("BarsHash(base): %v", err)
	}
	h2, err := BarsHash(withGap)
	if err != nil {
		t.Fatalf("BarsHash(withGap): %v", err)
	}
	if h1 != h2 {
		t.Errorf("IsGap/GapType mutated bars_hash:\n  base=%s\n  withGap=%s", h1, h2)
	}
}

// TestBarsHashSensitiveToPrice verifies the hash is not degenerate:
// any OHLCV change must produce a different hash.
func TestBarsHashSensitiveToPrice(t *testing.T) {
	a := []domain.Bar{{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 10.5, Volume: 100.0}}
	b := []domain.Bar{{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 99.9, Volume: 100.0}}

	ha, err := BarsHash(a)
	if err != nil {
		t.Fatalf("BarsHash(a): %v", err)
	}
	hb, err := BarsHash(b)
	if err != nil {
		t.Fatalf("BarsHash(b): %v", err)
	}
	if ha == hb {
		t.Error("different Close values must produce different bars_hash")
	}
}

// TestBarsHashDeterministic verifies the same input produces the same hash.
func TestBarsHashDeterministic(t *testing.T) {
	bars := []domain.Bar{
		{OpenTime: 1000, Open: 10.0, High: 11.0, Low: 9.0, Close: 10.5, Volume: 100.0},
	}
	h1, err := BarsHash(bars)
	if err != nil {
		t.Fatalf("first BarsHash: %v", err)
	}
	h2, err := BarsHash(bars)
	if err != nil {
		t.Fatalf("second BarsHash: %v", err)
	}
	if h1 != h2 {
		t.Errorf("same input produced different hashes: %s vs %s", h1, h2)
	}
}

// TestBarsHashFormat verifies the output is a 64-character lower-hex string.
func TestBarsHashFormat(t *testing.T) {
	bars := []domain.Bar{{OpenTime: 1, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1}}
	h, err := BarsHash(bars)
	if err != nil {
		t.Fatalf("BarsHash: %v", err)
	}
	if len(h) != 64 {
		t.Errorf("expected 64-char hex hash, got len=%d: %s", len(h), h)
	}
	for _, c := range h {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			t.Errorf("non-lower-hex character %q in hash %s", c, h)
			break
		}
	}
}
