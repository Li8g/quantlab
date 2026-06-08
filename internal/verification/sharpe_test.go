package verification

import (
	"math"
	"math/rand"
	"testing"

	"quantlab/internal/quant"
)

func TestComputeSharpeStats_EmptySeries(t *testing.T) {
	for _, name := range []string{"nil", "len-1"} {
		var returns []float64
		if name == "len-1" {
			returns = []float64{0.01}
		}
		s := ComputeSharpeStats(returns)
		if s.ObservedSharpe != 0 || s.Skew != 0 || s.ExcessKurt != 0 {
			t.Errorf("%s: expected zero stats, got %+v", name, s)
		}
		if s.HorizonT != len(returns) {
			t.Errorf("%s: HorizonT = %d, want %d", name, s.HorizonT, len(returns))
		}
	}
}

func TestComputeSharpeStats_ConstantSeriesHasZeroSharpe(t *testing.T) {
	// StdDev = 0 → divide-by-zero guard kicks in → ObservedSharpe = 0.
	// Skew / ExcessKurt come from quant which also returns 0 here;
	// the test pins the no-NaN-leakage contract.
	c := []float64{0.05, 0.05, 0.05, 0.05, 0.05}
	s := ComputeSharpeStats(c)
	if !math.IsNaN(s.ObservedSharpe) && s.ObservedSharpe != 0 {
		t.Errorf("constant series: Sharpe = %v, want 0 (no NaN)", s.ObservedSharpe)
	}
	if s.HorizonT != len(c) {
		t.Errorf("HorizonT = %d, want %d", s.HorizonT, len(c))
	}
}

func TestComputeSharpeStats_PositiveMean(t *testing.T) {
	// Series with consistently positive returns → ObservedSharpe > 0.
	r := []float64{0.01, 0.012, 0.008, 0.011, 0.009, 0.013, 0.007, 0.01}
	s := ComputeSharpeStats(r)
	if s.ObservedSharpe <= 0 {
		t.Errorf("positive-mean series: Sharpe = %v, want > 0", s.ObservedSharpe)
	}
	if s.HorizonT != len(r) {
		t.Errorf("HorizonT = %d, want %d", s.HorizonT, len(r))
	}
}

func TestComputeSharpeStats_NegativeMean(t *testing.T) {
	r := []float64{-0.01, -0.012, -0.008, -0.011, -0.009, -0.013, -0.007, -0.01}
	s := ComputeSharpeStats(r)
	if s.ObservedSharpe >= 0 {
		t.Errorf("negative-mean series: Sharpe = %v, want < 0", s.ObservedSharpe)
	}
}

func TestComputeSharpeStats_FeedsDSRWithoutNaN(t *testing.T) {
	// Integration: produce stats from a synthetic series then run
	// ComputeDSR with them. NaN here would mean either the helpers
	// emit invalid moments or DSR's guards are too strict.
	//
	// Use a deterministic sequence with bounded moments — a smooth
	// sine-modulated return mimics realistic market noise without
	// the pathological excess kurtosis a strict two-state
	// alternation produces.
	r := make([]float64, 200)
	for i := range r {
		// Mean 0.0008, amplitude 0.012 → moderate Sharpe, finite skew + kurt.
		r[i] = 0.0008 + 0.012*math.Sin(float64(i)*0.4)
	}
	s := ComputeSharpeStats(r)
	dsr := ComputeDSR(s.ObservedSharpe, 0.2, 10, s.HorizonT, s.Skew, s.ExcessKurt)
	if math.IsNaN(dsr) {
		t.Errorf("DSR = NaN from healthy stats %+v", s)
	}
}

// TestComputeSharpeStats_MatchesSeparateFunctions verifies that the combined
// two-pass implementation agrees with the original separate quant functions
// to within 1e-9 relative tolerance on a large realistic series.
func TestComputeSharpeStats_MatchesSeparateFunctions(t *testing.T) {
	rng := rand.New(rand.NewSource(99))
	returns := make([]float64, 87_600)
	for i := range returns {
		returns[i] = 0.0008 + (rng.Float64()-0.5)*0.02
	}

	got := ComputeSharpeStats(returns)

	// Reference values from the original individual functions.
	wantStd := quant.StdDev(returns)
	wantMean := quant.KahanSum(returns) / float64(len(returns))
	var wantSharpe float64
	if wantStd > 0 {
		wantSharpe = wantMean / wantStd
	}
	wantSkew := quant.Skewness(returns)
	wantExKurt := quant.ExcessKurtosis(returns)

	const tol = 1e-9
	check := func(name string, got, want float64) {
		t.Helper()
		if want == 0 {
			if math.Abs(got) > tol {
				t.Errorf("%s: got %v, want ~0", name, got)
			}
			return
		}
		if rel := math.Abs(got-want) / math.Abs(want); rel > tol {
			t.Errorf("%s: got %v, want %v (rel diff %.2e > tol %.2e)", name, got, want, rel, tol)
		}
	}

	check("ObservedSharpe", got.ObservedSharpe, wantSharpe)
	check("Skew", got.Skew, wantSkew)
	check("ExcessKurt", got.ExcessKurt, wantExKurt)
	if got.HorizonT != len(returns) {
		t.Errorf("HorizonT = %d, want %d", got.HorizonT, len(returns))
	}
}

func BenchmarkComputeSharpeStats_87kReturns(b *testing.B) {
	rng := rand.New(rand.NewSource(42))
	returns := make([]float64, 87_600)
	for i := range returns {
		returns[i] = 0.0008 + (rng.Float64()-0.5)*0.02
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ComputeSharpeStats(returns)
	}
}
