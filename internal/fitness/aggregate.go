// Package fitness aggregates per-window CrucibleResult into a single
// ScoreTotal. This logic lives strictly on the engine side — strategies
// physically cannot write ScoreTotal because RawEvaluateResult omits the
// field (see internal/strategy/evolvable.go package doc).
//
// Formula references:
//   - Part I §I-3.7  (window-weighted scoring)
//   - Part I §I-3.9  (consistency penalty, v1-raw-std)
//
// FitnessVersion v1-raw-std semantics (frozen — bumping the version
// requires migrating persisted Challengers):
//   - Window weights are FIXED (6m=0.10, 2y=0.20, 5y=0.30, 10y=0.40);
//     missing/skipped windows contribute 0 and the denominator is NOT
//     renormalized. This is intentional: a Challenger with insufficient
//     bars is naturally penalized via the missing weighted contribution.
//   - λ_cons = 0.3 applied against the raw (population) standard deviation
//     of the per-window Values (Bessel correction NOT applied).
//   - len(validScores) < 2 → consistency penalty = 0.
package fitness

import (
	"math"

	"quantlab/internal/resultpkg"
)

// AggregateScoreTotal turns []CrucibleResult into ScoreTotal per the v1-
// raw-std spec. Any self-Fatal window short-circuits to ScoreTotal.Fatal
// (Sum-type contagion); cascade-skipped windows are silently dropped from
// the weighted sum (their weight contributes 0).
//
// fitnessVer is accepted for forward-compat audit (e.g. logging which
// formula produced this score) but does not switch behavior in v1.
func AggregateScoreTotal(
	windows []resultpkg.CrucibleResult,
	weights map[resultpkg.WindowName]float64,
	lambdaCons float64,
	fitnessVer string,
) resultpkg.ScoreTotal {
	_ = fitnessVer // reserved; behaviour switches when v2-zscore lands

	for _, w := range windows {
		if w.Score.Fatal {
			return resultpkg.ScoreTotal{Fatal: true}
		}
	}

	validScores := make([]float64, 0, len(windows))
	var scoreRaw float64
	for _, w := range windows {
		if w.SkippedBy != nil || w.Score.Value == nil {
			continue
		}
		validScores = append(validScores, *w.Score.Value)
		scoreRaw += weights[w.Window] * (*w.Score.Value)
	}

	var sigma float64
	if len(validScores) >= 2 {
		sigma = populationStdDev(validScores)
	}
	val := scoreRaw - lambdaCons*sigma
	return resultpkg.ScoreTotal{
		Fatal:              false,
		Value:              &val,
		ScoreRaw:           &scoreRaw,
		ConsistencyPenalty: &sigma,
	}
}

// populationStdDev computes σ with N in the denominator (not N-1).
// This is the "raw" in v1-raw-std.
func populationStdDev(xs []float64) float64 {
	n := float64(len(xs))
	var sum float64
	for _, x := range xs {
		sum += x
	}
	mean := sum / n
	var ss float64
	for _, x := range xs {
		d := x - mean
		ss += d * d
	}
	return math.Sqrt(ss / n)
}
