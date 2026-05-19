// Per-challenger Sharpe statistics helper for the DSR pipeline.
// Source-of-truth: docs/Coding-plan-dev-phases-prompts_v3_2_2.md
// Phase 5B step 5 ("计算 SharpeBank 输入") + §I-4.2.
//
// ComputeSharpeStats turns a per-bar log-return series into the
// four scalars the SharpeBank table stores: ObservedSharpe + Skew +
// ExcessKurt + HorizonT. The DSR closed form (dsr.go) consumes all
// four directly.
//
// Sharpe is per-bar (not annualised). Annualisation is a display
// concern handled by the UI when the user picks a horizon. The
// SharpeBank table records the raw per-bar number so cross-bar-
// interval comparisons stay honest.
package verification

import (
	"quantlab/internal/quant"
)

// SharpeStats bundles the four DSR inputs derived from one
// challenger's return series. HorizonT mirrors len(returns) for
// audit symmetry — callers should never pass returns with stale
// lengths.
type SharpeStats struct {
	ObservedSharpe float64
	Skew           float64
	ExcessKurt     float64
	HorizonT       int
}

// ComputeSharpeStats returns the four DSR inputs from a per-bar
// log-return series. The series is expected to span the longest
// evaluated window only (typically 10y per §I-4.2 "T = 回测 horizon")
// — concatenating shorter windows would double-count overlapping
// bars and bias the moment estimators.
//
// Degenerate inputs:
//   - len(returns) < 2  → ObservedSharpe = 0 (StdDev returns 0)
//   - constant series   → StdDev = 0 → ObservedSharpe defaults to 0
//                         (avoids 0/0 NaN propagation into DSR)
//   - all-NaN inputs    → caller responsibility; this function
//                         doesn't sanitise
//
// Skew + ExcessKurt use quant.Skewness / quant.ExcessKurtosis which
// are biased moment estimators (g1 / g2 in standard notation).
// Bailey & Prado's DSR derivation assumes biased estimators per
// the 2014 paper's Eq. 12.
func ComputeSharpeStats(returns []float64) SharpeStats {
	n := len(returns)
	if n < 2 {
		return SharpeStats{HorizonT: n}
	}
	std := quant.StdDev(returns)
	mean := meanOf(returns)
	var sharpe float64
	if std > 0 {
		sharpe = mean / std
	}
	return SharpeStats{
		ObservedSharpe: sharpe,
		Skew:           quant.Skewness(returns),
		ExcessKurt:     quant.ExcessKurtosis(returns),
		HorizonT:       n,
	}
}

// meanOf is a tiny local helper — quant doesn't export a public
// Mean function and KahanSum is internal-only here.
func meanOf(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var sum float64
	for _, x := range xs {
		sum += x
	}
	return sum / float64(len(xs))
}
