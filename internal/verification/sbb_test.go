package verification

import (
	"math"
	"reflect"
	"testing"
)

// genAR1 builds a deterministic AR(1) series x_t = φ·x_{t-1} + ε_t with
// i.i.d. uniform noise (the theoretical acf(k)=φ^|k| holds for any i.i.d.
// innovation, so uniform noise is enough and stays seed-deterministic).
// burnIn samples are discarded so the series is stationary.
func genAR1(phi float64, n, burnIn int, seed uint64) []float64 {
	rng := splitMix64{state: seed}
	x := 0.0
	out := make([]float64, 0, n)
	for i := 0; i < burnIn+n; i++ {
		eps := rng.float64() - 0.5
		x = phi*x + eps
		if i >= burnIn {
			out = append(out, x)
		}
	}
	return out
}

func TestOptimalBlockLength_AR1MatchesTheory(t *testing.T) {
	const (
		phi = 0.5
		n   = 10000
	)
	series := genAR1(phi, n, 1000, 0xC0FFEE)
	raw, ok := optimalBlockLengthRaw(series)
	if !ok {
		t.Fatal("optimalBlockLengthRaw: estimator failed on AR(1), want ok")
	}
	// Asymptotic stationary-bootstrap optimum: b = (4φ²n/(1−φ²)²)^(1/3).
	theory := math.Cbrt(4 * phi * phi * n / math.Pow(1-phi*phi, 2))
	lo, hi := 0.7*theory, 1.3*theory
	t.Logf("raw=%.3f theory=%.3f band=[%.3f,%.3f]", raw, theory, lo, hi)
	if raw < lo || raw > hi {
		t.Errorf("raw block length %.3f outside ±30%% of theory %.3f [%.3f,%.3f]",
			raw, theory, lo, hi)
	}
}

func TestOptimalBlockLength_TruncatesToBounds(t *testing.T) {
	// White noise ⇒ acf insignificant almost immediately ⇒ tiny raw
	// estimate ⇒ must clamp up to the floor (100), never below.
	noise := genAR1(0.0, 5000, 100, 0x1234)
	got := OptimalBlockLength(noise)
	if got < sbbBlockLenMin || got > sbbBlockLenMax {
		t.Errorf("OptimalBlockLength(white noise)=%d, want ∈[%d,%d]",
			got, sbbBlockLenMin, sbbBlockLenMax)
	}

	// Too-short series ⇒ estimator can't run ⇒ fallback (300, in-bounds).
	if got := OptimalBlockLength([]float64{0.1}); got != SbbBlockLenFallback {
		t.Errorf("OptimalBlockLength(tiny)=%d, want fallback %d", got, SbbBlockLenFallback)
	}
}

func TestRunMonteCarlo_Deterministic(t *testing.T) {
	series := genAR1(0.3, 2000, 200, 0xABCD)
	a := RunMonteCarlo(series, 50, 500, 42, 0.70)
	b := RunMonteCarlo(series, 50, 500, 42, 0.70)
	if !reflect.DeepEqual(a, b) {
		t.Errorf("same seed produced different reports:\n a=%+v\n b=%+v", a, b)
	}
	if c := RunMonteCarlo(series, 50, 500, 43, 0.70); reflect.DeepEqual(a, c) {
		t.Error("different seed produced identical report (RNG not seeded)")
	}
	if a.Version != StressSummaryVersionV1 {
		t.Errorf("Version=%q, want %q", a.Version, StressSummaryVersionV1)
	}
}

func TestRunMonteCarlo_RuinSemantics(t *testing.T) {
	const n = 600
	down := make([]float64, n)
	up := make([]float64, n)
	for i := range down {
		down[i] = -0.01 // steady decay: equity = exp(-0.01·t) crosses 0.30 well within n
		up[i] = 0.01    // steady growth: never falls, no drawdown
	}

	d := RunMonteCarlo(down, 50, 200, 7, 0.70) // floor = 0.30
	if d.RuinProbability != 1.0 {
		t.Errorf("all-negative series RuinProbability=%.4f, want 1.0", d.RuinProbability)
	}
	if d.FinalEquityP50 >= 1.0 {
		t.Errorf("all-negative series FinalEquityP50=%.4f, want <1", d.FinalEquityP50)
	}

	u := RunMonteCarlo(up, 50, 200, 7, 0.70)
	if u.RuinProbability != 0.0 {
		t.Errorf("all-positive series RuinProbability=%.4f, want 0", u.RuinProbability)
	}
	if u.FinalEquityP50 <= 1.0 {
		t.Errorf("all-positive series FinalEquityP50=%.4f, want >1", u.FinalEquityP50)
	}
	if u.WorstMDD1Pct != 0.0 {
		t.Errorf("monotone-up series WorstMDD1Pct=%.4f, want 0", u.WorstMDD1Pct)
	}
}

func TestRunMonteCarlo_Degenerate(t *testing.T) {
	for _, tc := range []struct {
		name    string
		returns []float64
		nIter   int
	}{
		{"nil series", nil, 100},
		{"empty series", []float64{}, 100},
		{"zero iters", []float64{0.01, -0.01}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := RunMonteCarlo(tc.returns, 50, tc.nIter, 1, 0.70)
			if rep.Version != StressSummaryVersionV1 {
				t.Errorf("Version=%q, want %q", rep.Version, StressSummaryVersionV1)
			}
			for _, f := range []float64{
				rep.RuinProbability, rep.FinalEquityP5, rep.FinalEquityP50,
				rep.FinalEquityP95, rep.WorstMDD1Pct,
			} {
				if math.IsNaN(f) || math.IsInf(f, 0) {
					t.Errorf("degenerate report leaked non-finite value: %+v", rep)
					break
				}
			}
		})
	}
}
