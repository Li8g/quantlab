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
	"math"

	"quantlab/internal/quant"
	"quantlab/internal/resultpkg"
)

// SharpeStats is an alias kept for source compatibility. The canonical
// type lives in resultpkg so RawEvaluateResult can carry it without
// crossing the verification→quant→resultpkg cycle boundary.
type SharpeStats = resultpkg.SharpeStats

// ComputeSharpeStats returns the four DSR inputs from a per-bar
// log-return series. The series is expected to span the longest
// evaluated window only (typically 10y per §I-4.2 "T = 回测 horizon")
// — concatenating shorter windows would double-count overlapping
// bars and bias the moment estimators.
//
// Degenerate inputs:
//   - len(returns) < 2  → ObservedSharpe = 0 (StdDev returns 0)
//   - constant series   → StdDev = 0 → ObservedSharpe defaults to 0
//     (avoids 0/0 NaN propagation into DSR)
//   - all-NaN inputs    → caller responsibility; this function
//     doesn't sanitise
//
// Skew + ExcessKurt use quant.Skewness / quant.ExcessKurtosis which
// are biased moment estimators (g1 / g2 in standard notation).
// Bailey & Prado's DSR derivation assumes biased estimators per
// the 2014 paper's Eq. 12.
func ComputeSharpeStats(returns []float64) resultpkg.SharpeStats {
	n := len(returns)
	if n < 2 {
		return resultpkg.SharpeStats{HorizonT: n}
	}

	// Pass 1: mean via Kahan compensated sum — same algorithm as before.
	mean := quant.KahanSum(returns) / float64(n)

	// Pass 2: accumulate Σd², Σd³, Σd⁴ simultaneously with three Kahan
	// compensators. Replaces the 7 separate KahanSum calls and 5 heap
	// allocations that StdDev/Skewness/ExcessKurtosis formerly required.
	// mean is computed once and shared — eliminates 3 redundant mean passes.
	var s2, c2, s3, c3, s4, c4 float64
	for _, x := range returns {
		d := x - mean
		d2 := d * d
		d3 := d2 * d
		d4 := d2 * d2
		y := d2 - c2; t := s2 + y; c2 = (t - s2) - y; s2 = t
		y = d3 - c3; t = s3 + y; c3 = (t - s3) - y; s3 = t
		y = d4 - c4; t = s4 + y; c4 = (t - s4) - y; s4 = t
	}

	// StdDev uses n-1 (sample); Skew/ExKurt use n (population biased,
	// matching Bailey & Prado's DSR derivation per Eq. 12).
	var std float64
	if s2 > 0 {
		std = math.Sqrt(s2 / float64(n-1))
	}
	var sharpe float64
	if std > 0 {
		sharpe = mean / std
	}

	var skew float64
	if n >= 3 {
		if m2 := s2 / float64(n); m2 > 0 {
			skew = (s3 / float64(n)) / math.Pow(m2, 1.5)
		}
	}

	var exKurt float64
	if n >= 4 {
		if m2 := s2 / float64(n); m2 > 0 {
			exKurt = (s4/float64(n))/(m2*m2) - 3.0
		}
	}

	return resultpkg.SharpeStats{
		ObservedSharpe: sharpe,
		Skew:           skew,
		ExcessKurt:     exKurt,
		HorizonT:       n,
	}
}
