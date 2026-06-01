// Stationary Block Bootstrap (SBB) Monte Carlo stress test. Source-of-
// truth: docs/Coding-plan-dev-phases-prompts_v3_2_2.md §I-4.3.
//
// Two pieces:
//
//  1. OptimalBlockLength — Politis & White (2004) + Patton, Politis &
//     White (2009) automatic block-length selector for the stationary
//     bootstrap. Six-step procedure (§I-4.3): ACF → first insignificant
//     run → flat-top-windowed g and d_SB sums → b_opt = (2g²/d_SB·n)^⅓,
//     truncated to [100, 1440]. Estimation failure falls back to 300.
//
//  2. RunMonteCarlo — resamples the per-bar log-return series into nIter
//     synthetic equity paths via the stationary (circular) block
//     bootstrap with geometric block lengths (mean = blockLenMean), and
//     summarises the path distribution: ruin probability, 5/50/95-pct
//     final equity, worst-1% max drawdown.
//
// The MCReport marshals onto resultpkg.VerificationLayer.StressSummary
// (a json.RawMessage), mirroring how DSRSummary feeds DSRSummary —
// stress_summary is a Promote-only display field, never a GA fitness
// input.
//
// [INVENTED v1] The MCReport shape and the ruin definition are NOT part
// of the frozen v5.3.3 schema (stress_summary is a "宽松对象，后续收紧"
// per 数据契约 §7). Version="stress-v1" tags it so a future reader can
// pin the shape. Two choices recorded here:
//
//   - ruin = "absolute-from-start": a path is ruined if its running
//     equity (starting at 1.0) ever falls to ≤ 1−FatalMDD. This reuses
//     the system-wide FatalMDD catastrophe semantics (cascade Fatal,
//     fatal_reason=mdd_exceeded), and is orthogonal to WorstMDD1Pct
//     (which is drawdown-from-running-peak). A terminal "loss
//     probability" (final equity < initial) is a possible future field,
//     deliberately omitted for now (no consumer yet).
//   - nIter default = 1000 (caller-supplied; cheap as a post-epoch
//     one-shot).
package verification

import (
	"math"
	"sort"

	"quantlab/internal/quant"
)

// StressSummaryVersionV1 tags the [INVENTED v1] MCReport shape.
const StressSummaryVersionV1 = "stress-v1"

// SbbBlockLenFallback is the §I-5 default block length used when the
// Politis-White estimator fails (degenerate ACF, too-short series). The
// SaaS config field of the same name lets operators override the value
// passed to RunMonteCarlo; this const is the pure-function fallback.
const SbbBlockLenFallback = 300

// sbbBlockLenMin / sbbBlockLenMax are the §I-4.3 truncation bounds.
const (
	sbbBlockLenMin = 100
	sbbBlockLenMax = 1440
)

// MCReport is the stationary-block-bootstrap stress summary. It marshals
// (by the caller) into VerificationLayer.StressSummary.
//
// FinalEquity* are multiples of the starting equity (1.0 ⇒ break-even).
// WorstMDD1Pct ∈ [0,1] is the 99th-percentile path max-drawdown (the
// worst 1% of paths). RuinProbability ∈ [0,1] is the fraction of paths
// whose running equity ever fell to ≤ 1−FatalMDD.
type MCReport struct {
	Version         string  `json:"version"`
	NIter           int     `json:"n_iter"`
	BlockLenMean    int     `json:"block_len_mean"`
	Seed            uint64  `json:"seed"`
	RuinProbability float64 `json:"ruin_probability"`
	FinalEquityP5   float64 `json:"final_equity_p5"`
	FinalEquityP50  float64 `json:"final_equity_p50"`
	FinalEquityP95  float64 `json:"final_equity_p95"`
	WorstMDD1Pct    float64 `json:"worst_mdd_1pct"`
}

// OptimalBlockLength returns the Politis-White stationary-bootstrap
// block length for the log-return series, truncated to [100, 1440].
// Returns SbbBlockLenFallback (300) when the estimator cannot run
// (series too short for the ACF lag budget, or a degenerate spectral
// estimate). See optimalBlockLengthRaw for the untruncated estimate the
// theory tests pin.
func OptimalBlockLength(returns []float64) int {
	raw, ok := optimalBlockLengthRaw(returns)
	if !ok {
		return SbbBlockLenFallback
	}
	v := int(math.Round(raw))
	if v < sbbBlockLenMin {
		return sbbBlockLenMin
	}
	if v > sbbBlockLenMax {
		return sbbBlockLenMax
	}
	return v
}

// optimalBlockLengthRaw is the untruncated §I-4.3 estimate. Split out so
// TestOptimalBlockLength_AR1MatchesTheory can compare against the closed
// form without the [100,1440] clamp masking the result. ok=false ⇒ the
// caller should fall back.
func optimalBlockLengthRaw(returns []float64) (float64, bool) {
	n := len(returns)
	if n < 2 {
		return 0, false
	}
	log10n := math.Log10(float64(n))
	// Step 1: ACF up to lag = ceil(√n + log₁₀(n)).
	maxLag := int(math.Ceil(math.Sqrt(float64(n)) + log10n))
	if maxLag < 1 || maxLag >= n {
		return 0, false
	}
	acf := quant.ACF(returns, maxLag) // acf[0]=1

	// Step 2: smallest lag m̂ after which k_n consecutive lags are all
	// insignificant (|acf| < 2·√(log₁₀(n)/n)).
	bound := 2 * math.Sqrt(log10n/float64(n))
	kn := int(math.Round(log10n))
	if kn < 5 {
		kn = 5
	}
	m := 0
	for j := 1; j+kn-1 <= maxLag; j++ {
		allInsig := true
		for k := j; k < j+kn; k++ {
			if math.Abs(acf[k]) >= bound {
				allInsig = false
				break
			}
		}
		if allInsig {
			m = j
			break
		}
	}
	if m < 1 {
		return 0, false
	}

	// Steps 3-4: flat-top-windowed sums over k=-m..m (symmetric, so
	// fold the k>0 terms ×2; the k=0 term is λ(0)·acf[0]=1).
	//   g    = Σ λ(k/m)·|k|·acf[|k|]
	//   sumW = Σ λ(k/m)·acf[|k|]   ⇒ d_SB = 2·sumW²
	g := 0.0
	sumW := acf[0] // k=0
	for k := 1; k <= m; k++ {
		w := flatTop(float64(k) / float64(m))
		g += 2 * w * float64(k) * acf[k]
		sumW += 2 * w * acf[k]
	}
	dSB := 2 * sumW * sumW
	if dSB <= 0 || math.IsNaN(g) || math.IsInf(g, 0) {
		return 0, false
	}

	// Step 5: b_opt = (2·g²/d_SB · n)^(1/3).
	bOpt := math.Cbrt(2 * g * g / dSB * float64(n))
	if math.IsNaN(bOpt) || math.IsInf(bOpt, 0) || bOpt <= 0 {
		return 0, false
	}
	return bOpt, true
}

// flatTop is the Politis-White flat-top lag window λ(t):
//
//	λ(t) = 1            |t| ≤ ½
//	     = 2(1 − |t|)   ½ < |t| ≤ 1
//	     = 0            |t| > 1
func flatTop(t float64) float64 {
	a := math.Abs(t)
	switch {
	case a <= 0.5:
		return 1
	case a <= 1:
		return 2 * (1 - a)
	default:
		return 0
	}
}

// RunMonteCarlo resamples returns (per-bar log returns) into nIter
// synthetic equity paths via the stationary block bootstrap with
// geometric block lengths (mean = blockLenMean) over the circular index,
// and summarises the path distribution. fatalMDD sets the ruin floor
// (1−fatalMDD); pass plan.FatalMDD so stress-ruin matches the cascade's
// Fatal threshold. seed makes the report reproducible.
//
// Extends the §I-4.3 prompt signature with fatalMDD because the ruin
// metric is defined against the system FatalMDD (see file header).
//
// Degenerate inputs (empty series, nIter ≤ 0) return a zero-valued
// report tagged with Version — never panics, never leaks NaN.
func RunMonteCarlo(returns []float64, blockLenMean, nIter int, seed uint64, fatalMDD float64) MCReport {
	rep := MCReport{
		Version:      StressSummaryVersionV1,
		NIter:        nIter,
		BlockLenMean: blockLenMean,
		Seed:         seed,
	}
	n := len(returns)
	if n == 0 || nIter <= 0 {
		return rep
	}
	if blockLenMean < 1 {
		blockLenMean = 1
	}
	ruinFloor := 1 - fatalMDD
	pGeom := 1.0 / float64(blockLenMean)
	logOneMinusP := math.Log(1 - pGeom) // <0 for blockLenMean>1; blockLenMean=1 ⇒ pGeom=1 handled below

	rng := splitMix64{state: seed}
	finals := make([]float64, nIter)
	mdds := make([]float64, nIter)
	ruinCount := 0

	for it := 0; it < nIter; it++ {
		cumLog := 0.0   // log equity; equity = exp(cumLog), starts at 1.0
		peak := 1.0     // running peak equity, for drawdown-from-peak
		maxDD := 0.0     // worst drawdown-from-peak on this path
		ruined := false // running equity ever ≤ ruinFloor (absolute-from-start)

		filled := 0
		for filled < n {
			start := int(rng.next() % uint64(n))
			blockLen := geomBlockLen(rng.float64(), pGeom, logOneMinusP)
			for j := 0; j < blockLen && filled < n; j++ {
				cumLog += returns[(start+j)%n]
				equity := math.Exp(cumLog)
				if equity > peak {
					peak = equity
				} else if dd := (peak - equity) / peak; dd > maxDD {
					maxDD = dd
				}
				if equity <= ruinFloor {
					ruined = true
				}
				filled++
			}
		}
		finals[it] = math.Exp(cumLog)
		mdds[it] = maxDD
		if ruined {
			ruinCount++
		}
	}

	sort.Float64s(finals)
	sort.Float64s(mdds)
	rep.RuinProbability = float64(ruinCount) / float64(nIter)
	rep.FinalEquityP5 = percentile(finals, 5)
	rep.FinalEquityP50 = percentile(finals, 50)
	rep.FinalEquityP95 = percentile(finals, 95)
	rep.WorstMDD1Pct = percentile(mdds, 99)
	return rep
}

// geomBlockLen samples a geometric block length ≥1 with mean 1/pGeom
// from a uniform u∈[0,1). logOneMinusP = ln(1−pGeom) is precomputed.
// pGeom==1 (blockLenMean==1) degenerates to fixed length 1.
func geomBlockLen(u, pGeom, logOneMinusP float64) int {
	if pGeom >= 1 {
		return 1
	}
	l := int(math.Floor(math.Log(1-u)/logOneMinusP)) + 1
	if l < 1 {
		return 1
	}
	return l
}

// percentile is the linear-interpolation quantile of an ascending-sorted
// slice; p ∈ [0,100].
func percentile(sorted []float64, p float64) float64 {
	switch len(sorted) {
	case 0:
		return 0
	case 1:
		return sorted[0]
	}
	rank := p / 100 * float64(len(sorted)-1)
	lo := int(math.Floor(rank))
	hi := int(math.Ceil(rank))
	if lo == hi {
		return sorted[lo]
	}
	frac := rank - float64(lo)
	return sorted[lo]*(1-frac) + sorted[hi]*frac
}

// splitMix64 is the local deterministic RNG for the bootstrap resampler.
// verification cannot import internal/engine (would cycle), so it carries
// its own splitter — same self-contained-numerics precedent as dsr.go's
// Acklam approximation. Same seed ⇒ byte-identical MCReport.
type splitMix64 struct{ state uint64 }

func (s *splitMix64) next() uint64 {
	s.state += 0x9e3779b97f4a7c15
	z := s.state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}

// float64 returns a uniform value in [0,1) from the top 53 bits.
func (s *splitMix64) float64() float64 {
	return float64(s.next()>>11) / (1 << 53)
}
