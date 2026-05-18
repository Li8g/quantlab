package quant

import "quantlab/internal/domain"

// ExtractCloses returns the Close prices from a Bar slice, preserving order.
// Phase 3 contract: strategy kernels are forbidden from importing domain.Bar
// directly (see CLAUDE.md "strategy layer must never import strategy
// internals"); they consume closes via this helper instead. Keeping the
// projection in one place also gives a single audit point if the contract
// later widens (e.g. HLC3 or VWAP-weighted closes).
func ExtractCloses(bars []domain.Bar) []float64 {
	out := make([]float64, len(bars))
	for i, b := range bars {
		out[i] = b.Close
	}
	return out
}

// ExtractTimestamps returns the OpenTime (UTC ms) of each bar. Same rationale
// as ExtractCloses — strategy kernels need timestamps for time-based logic
// but must not touch Bar directly.
func ExtractTimestamps(bars []domain.Bar) []int64 {
	out := make([]int64, len(bars))
	for i, b := range bars {
		out[i] = b.OpenTime
	}
	return out
}
