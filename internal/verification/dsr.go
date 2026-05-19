// Deflated Sharpe Ratio (Bailey & López de Prado 2014). Source-of-
// truth: docs/Coding-plan-dev-phases-prompts_v3_2_2.md §I-4.2.
//
// Closed-form formula (γ_em = 0.5772 Euler-Mascheroni):
//
//	SR₀  = √Var(Sharpe) × [(1 − γ_em)·Φ⁻¹(1 − 1/N)
//	                     +  γ_em ·Φ⁻¹(1 − 1/(N·e))]
//	σ_SR = √[(1 − skew·SR_obs + (excessKurt/4)·SR_obs²) / (T − 1)]
//	DSR  = Φ((SR_obs − SR₀) / σ_SR)
//
// Reliability gate: N < 5 returns math.NaN() so the front-end shows a
// "积累中" gray indicator (§I-4.4 DecisionColor). The caller must
// handle NaN explicitly — never write the NaN into PromoteLayer; per
// I-4.4 DSR lives on VerificationLayer.DSRSummary only.
//
// DSR is a Promote-only display field; it MUST NOT participate in GA
// in-generation fitness sorting (§I-4.2 final paragraph).
package verification

import (
	"math"
)

// DSRSummary is the JSON payload that lands on
// resultpkg.VerificationLayer.DSRSummary. The struct is exported
// here so callers (Phase 5D Epoch service) can build, marshal, and
// pass it as a json.RawMessage into BuildChallengerPackage.
//
// [INVENTED v1] — the spec mandates the field but doesn't pin the
// JSON shape. Field names are snake_case so the wire form matches
// the rest of resultpkg conventions.
type DSRSummary struct {
	DSR            float64 `json:"dsr"`
	ObservedSharpe float64 `json:"observed_sharpe"`
	SharpeVariance float64 `json:"sharpe_variance"`
	NTrials        int     `json:"n_trials"`
	HorizonT       int     `json:"horizon_t"`
	Skew           float64 `json:"skew"`
	ExcessKurt     float64 `json:"excess_kurt"`
}

// MinTrialsForDSR is the §I-4.2 reliability gate. Below this count
// ComputeDSR returns NaN regardless of the other inputs.
const MinTrialsForDSR = 5

// eulerMascheroni constant γ_em (Apéry-Mertens), to the precision
// the §I-4.2 formula uses literally.
const eulerMascheroni = 0.5772156649015329

// ComputeDSR evaluates the Bailey & López de Prado 2014 closed form.
// Returns math.NaN() when the SharpeBank sample count is below the
// §I-4.2 reliability threshold (5) or when any input would cause a
// numerically-undefined intermediate (variance ≤ 0, σ_SR ≤ 0).
//
// Parameters:
//
//	observedSharpe: SR_obs — current challenger's bar-level Sharpe.
//	sharpeVariance: Var(Sharpe) — variance of past SharpeBank entries
//	                for (strategy, pair); pulled from SharpeBankRepo.Stats.
//	nTrials:        N — accumulated SharpeBank size for the same key.
//	horizonT:       T — bar count over which observedSharpe was measured
//	                (typically the longest evaluated window's bar count).
//	skew, excessKurt: distributional moments of the per-bar return
//	                  series that produced observedSharpe.
func ComputeDSR(observedSharpe, sharpeVariance float64, nTrials, horizonT int, skew, excessKurt float64) float64 {
	if nTrials < MinTrialsForDSR {
		return math.NaN()
	}
	if sharpeVariance <= 0 || horizonT < 2 {
		return math.NaN()
	}

	N := float64(nTrials)
	stdSharpe := math.Sqrt(sharpeVariance)

	// SR₀ benchmark — the expected maximum Sharpe under the null of
	// random trials with the same variance, per the Bailey-Prado
	// rearrangement of the Gumbel maximum-order-statistic mean.
	phiInvOne := normalInverse(1.0 - 1.0/N)
	phiInvTwo := normalInverse(1.0 - 1.0/(N*math.E))
	sr0 := stdSharpe * ((1.0-eulerMascheroni)*phiInvOne + eulerMascheroni*phiInvTwo)

	// σ_SR — the standard deviation of observedSharpe accounting for
	// non-Gaussian return moments. The (T−1) denominator is the
	// degrees-of-freedom adjustment from the original paper.
	radicand := 1.0 - skew*observedSharpe + (excessKurt/4.0)*observedSharpe*observedSharpe
	if radicand <= 0 {
		return math.NaN()
	}
	sigmaSR := math.Sqrt(radicand / float64(horizonT-1))
	if sigmaSR <= 0 {
		return math.NaN()
	}

	return normalCDF((observedSharpe - sr0) / sigmaSR)
}

// normalCDF is the standard-normal cumulative distribution function
// Φ(x). Computed via math.Erf (Go stdlib) — that path is monotone,
// numerically stable for all finite x, and the prototype phase has
// no accuracy requirements tighter than 1e-9.
func normalCDF(x float64) float64 {
	return 0.5 * (1.0 + math.Erf(x/math.Sqrt2))
}

// normalInverse is the inverse standard-normal Φ⁻¹(p). Implemented
// via Acklam's 2003 rational approximation (relative error ≤ 1.15e-9
// over (0,1) except for extreme tails where the absolute error
// drifts to 1e-7). The §I-4.2 prompt explicitly suggests Acklam.
//
// Edge cases: p ≤ 0 → -Inf, p ≥ 1 → +Inf, p NaN → NaN. ComputeDSR
// never passes p that close to the boundaries because N ≥ 5 keeps
// (1 - 1/N) ≤ 0.8 and (1 - 1/(N·e)) ≤ 0.927; both are safely inside
// the central region.
func normalInverse(p float64) float64 {
	// Acklam coefficients.
	const (
		a1 = -3.969683028665376e+01
		a2 = +2.209460984245205e+02
		a3 = -2.759285104469687e+02
		a4 = +1.383577518672690e+02
		a5 = -3.066479806614716e+01
		a6 = +2.506628277459239e+00

		b1 = -5.447609879822406e+01
		b2 = +1.615858368580409e+02
		b3 = -1.556989798598866e+02
		b4 = +6.680131188771972e+01
		b5 = -1.328068155288572e+01

		c1 = -7.784894002430293e-03
		c2 = -3.223964580411365e-01
		c3 = -2.400758277161838e+00
		c4 = -2.549732539343734e+00
		c5 = +4.374664141464968e+00
		c6 = +2.938163982698783e+00

		d1 = +7.784695709041462e-03
		d2 = +3.224671290700398e-01
		d3 = +2.445134137142996e+00
		d4 = +3.754408661907416e+00

		pLow  = 0.02425
		pHigh = 1.0 - pLow
	)

	if math.IsNaN(p) {
		return math.NaN()
	}
	if p <= 0 {
		return math.Inf(-1)
	}
	if p >= 1 {
		return math.Inf(+1)
	}

	switch {
	case p < pLow:
		q := math.Sqrt(-2 * math.Log(p))
		return (((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) /
			((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	case p <= pHigh:
		q := p - 0.5
		r := q * q
		return (((((a1*r+a2)*r+a3)*r+a4)*r+a5)*r + a6) * q /
			(((((b1*r+b2)*r+b3)*r+b4)*r+b5)*r + 1)
	default:
		q := math.Sqrt(-2 * math.Log(1-p))
		return -(((((c1*q+c2)*q+c3)*q+c4)*q+c5)*q + c6) /
			((((d1*q+d2)*q+d3)*q+d4)*q + 1)
	}
}
