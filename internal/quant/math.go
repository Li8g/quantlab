package quant

import "math"

// EMA returns the exponential moving average of series with span = period.
// The smoothing factor α = 2/(period+1) follows the standard "RiskMetrics"
// convention. EMA[0] is seeded to series[0]; the output has the same length
// as the input. Callers that need a trailing-only EMA should discard the
// first `period-1` values.
//
// Panics if period <= 0 (programming error). Returns an empty slice for an
// empty input.
func EMA(series []float64, period int) []float64 {
	if period <= 0 {
		panic("quant.EMA: period must be > 0")
	}
	n := len(series)
	if n == 0 {
		return []float64{}
	}
	alpha := 2.0 / (float64(period) + 1.0)
	out := make([]float64, n)
	out[0] = series[0]
	for i := 1; i < n; i++ {
		out[i] = alpha*series[i] + (1.0-alpha)*out[i-1]
	}
	return out
}

// StdDev returns the sample standard deviation of series (n-1 denominator).
// Returns 0 for len(series) < 2.
//
// Uses a two-pass algorithm with KahanSum for the second pass to keep error
// bounded for long series — Welford's online variant would be faster but the
// engine's hot path computes StdDev rarely (per-Epoch, not per-bar).
func StdDev(series []float64) float64 {
	n := len(series)
	if n < 2 {
		return 0
	}
	mean := KahanSum(series) / float64(n)
	devsq := make([]float64, n)
	for i, x := range series {
		d := x - mean
		devsq[i] = d * d
	}
	variance := KahanSum(devsq) / float64(n-1)
	return math.Sqrt(variance)
}

// MAVAbsChange returns the mean of |x[i] - x[i-1]| over the whole series.
// Returns 0 for len(series) < 2.
//
// The framework doc names this primitive without specifying a window.
// This is the simplest interpretation (no window); callers that need a
// rolling window use MAVAbsChangeWindow.
func MAVAbsChange(series []float64) float64 {
	n := len(series)
	if n < 2 {
		return 0
	}
	abs := make([]float64, n-1)
	for i := 1; i < n; i++ {
		abs[i-1] = math.Abs(series[i] - series[i-1])
	}
	return KahanSum(abs) / float64(n-1)
}

// MAVAbsChangeWindow returns the mean absolute close-to-close change over
// the last `window` bars of series. It looks at the trailing `window`
// differences |series[i] - series[i-1]|, i.e. it consumes window+1 prices.
//
// Returns 0 when window <= 0, when window+1 > len(series), or when there
// are fewer than 2 prices available. Callers are expected to have enough
// warmup (MinEvalBars) that the degenerate cases are unreachable during
// normal evaluation; the lenient zero-return keeps unit tests cheap to
// write without hand-rolling guard paths.
func MAVAbsChangeWindow(series []float64, window int) float64 {
	n := len(series)
	if window <= 0 || n < 2 {
		return 0
	}
	if window+1 > n {
		return 0
	}
	start := n - window - 1
	abs := make([]float64, window)
	for i := 0; i < window; i++ {
		abs[i] = math.Abs(series[start+i+1] - series[start+i])
	}
	return KahanSum(abs) / float64(window)
}

// ClipFloat64 clamps x to [lo, hi]. Panics if lo > hi (programming error).
func ClipFloat64(x, lo, hi float64) float64 {
	if lo > hi {
		panic("quant.ClipFloat64: lo > hi")
	}
	if x < lo {
		return lo
	}
	if x > hi {
		return hi
	}
	return x
}

// RoundToUSDT rounds x to 8 decimal places — the smallest unit Binance uses
// for USDT quote amounts on most pairs. Using math.Round (banker's rounding
// would diverge across platforms) for deterministic results.
//
// If a strategy needs a different rounding step (e.g. pair-specific
// tickSize), introduce a separate helper rather than reparameterizing
// this one.
func RoundToUSDT(x float64) float64 {
	const scale = 1e8
	return math.Round(x*scale) / scale
}

// ACF returns the sample autocorrelation function ρ(0..maxLag).
// ρ(0) is always 1.0. Output length is maxLag+1.
//
// The denominator γ(0) uses the population variance (1/n Σ (x_i - μ)²),
// which matches the standard definition used by Politis & White (2004) for
// the SBB block-length estimator that consumes this output.
//
// O(n·maxLag). The framework doc mentions FFT — for the SBB use case
// maxLag is typically O(log n) and n is < 10000, so the direct method is
// faster than an FFT (cache-friendly, no allocation churn). Revisit if
// callers start passing maxLag > ~256.
//
// Panics if maxLag < 0, or if maxLag >= len(series) (degree of freedom 0).
func ACF(series []float64, maxLag int) []float64 {
	if maxLag < 0 {
		panic("quant.ACF: maxLag must be >= 0")
	}
	n := len(series)
	if n <= maxLag {
		panic("quant.ACF: len(series) must be > maxLag")
	}

	mean := KahanSum(series) / float64(n)
	centred := make([]float64, n)
	for i, x := range series {
		centred[i] = x - mean
	}

	sq := make([]float64, n)
	for i, c := range centred {
		sq[i] = c * c
	}
	gamma0 := KahanSum(sq) / float64(n)
	if gamma0 == 0 {
		// Constant series — autocorrelation is undefined; conventional
		// choice is ρ(0)=1, ρ(k>=1)=0.
		out := make([]float64, maxLag+1)
		out[0] = 1
		return out
	}

	out := make([]float64, maxLag+1)
	out[0] = 1
	for k := 1; k <= maxLag; k++ {
		prods := make([]float64, n-k)
		for i := 0; i < n-k; i++ {
			prods[i] = centred[i] * centred[i+k]
		}
		gammaK := KahanSum(prods) / float64(n)
		out[k] = gammaK / gamma0
	}
	return out
}

// Skewness returns the biased moment estimator g1 = m3 / m2^1.5,
// where m_k = (1/n) Σ (x_i - μ)^k. Population moments (n denominator).
// Returns 0 for len(series) < 3 or for a constant series.
func Skewness(series []float64) float64 {
	n := len(series)
	if n < 3 {
		return 0
	}
	mean := KahanSum(series) / float64(n)
	m2sum := make([]float64, n)
	m3sum := make([]float64, n)
	for i, x := range series {
		d := x - mean
		dd := d * d
		m2sum[i] = dd
		m3sum[i] = dd * d
	}
	m2 := KahanSum(m2sum) / float64(n)
	m3 := KahanSum(m3sum) / float64(n)
	if m2 == 0 {
		return 0
	}
	return m3 / math.Pow(m2, 1.5)
}

// ExcessKurtosis returns m4/m2^2 - 3 (population, biased estimator).
// Returns 0 for len(series) < 4 or for a constant series. A normal
// distribution has excess kurtosis 0; positive values indicate fatter tails.
func ExcessKurtosis(series []float64) float64 {
	n := len(series)
	if n < 4 {
		return 0
	}
	mean := KahanSum(series) / float64(n)
	m2sum := make([]float64, n)
	m4sum := make([]float64, n)
	for i, x := range series {
		d := x - mean
		dd := d * d
		m2sum[i] = dd
		m4sum[i] = dd * dd
	}
	m2 := KahanSum(m2sum) / float64(n)
	m4 := KahanSum(m4sum) / float64(n)
	if m2 == 0 {
		return 0
	}
	return m4/(m2*m2) - 3.0
}

// KahanSum computes Σ series with compensated summation. Iteration order
// matches input order, so the result is bit-identical given the same input
// — useful when determinism across reruns matters more than speed.
func KahanSum(series []float64) float64 {
	var sum, c float64
	for _, x := range series {
		y := x - c
		t := sum + y
		c = (t - sum) - y
		sum = t
	}
	return sum
}
