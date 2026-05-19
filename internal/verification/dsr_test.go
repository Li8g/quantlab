package verification

import (
	"math"
	"testing"
)

// ----- NormalCDF / NormalInverse round-trip -----

func TestNormalCDF_KnownValues(t *testing.T) {
	cases := []struct {
		x    float64
		want float64
	}{
		{0, 0.5},
		{1, 0.8413447460685429},
		{-1, 0.15865525393145707},
		{1.96, 0.9750021048517795}, // 95% one-sided
	}
	for _, c := range cases {
		got := normalCDF(c.x)
		if math.Abs(got-c.want) > 1e-12 {
			t.Errorf("normalCDF(%v) = %v, want %v", c.x, got, c.want)
		}
	}
}

func TestNormalInverse_RoundTripsCDF(t *testing.T) {
	// Φ⁻¹(Φ(x)) ≈ x for moderate x values. Acklam's accuracy spec
	// claims ≤ 1.15e-9 relative error in the central region.
	for _, x := range []float64{-3, -1.5, -0.5, 0, 0.25, 1, 2, 2.5} {
		p := normalCDF(x)
		got := normalInverse(p)
		if math.Abs(got-x) > 1e-8 {
			t.Errorf("inverse(cdf(%v)) = %v, want %v", x, got, x)
		}
	}
}

func TestNormalInverse_EdgeCases(t *testing.T) {
	if !math.IsInf(normalInverse(0), -1) {
		t.Error("Φ⁻¹(0) should be -Inf")
	}
	if !math.IsInf(normalInverse(1), +1) {
		t.Error("Φ⁻¹(1) should be +Inf")
	}
	if !math.IsNaN(normalInverse(math.NaN())) {
		t.Error("Φ⁻¹(NaN) should be NaN")
	}
}

// ----- ComputeDSR -----

func TestComputeDSR_NaNBelowMinTrials(t *testing.T) {
	// §I-4.2: N < 5 → NaN.
	for n := 0; n < MinTrialsForDSR; n++ {
		got := ComputeDSR(1.0, 0.1, n, 365, 0, 0)
		if !math.IsNaN(got) {
			t.Errorf("N=%d: ComputeDSR = %v, want NaN (reliability gate)", n, got)
		}
	}
}

func TestComputeDSR_ReturnsFiniteAtNGE5(t *testing.T) {
	// Synthetic scenario sized so DSR lands in the meaningful middle
	// of (0, 1). With σ_SR ∝ 1/√T, a horizon of 50 keeps the
	// standardised score moderate; observedSharpe is chosen just
	// above the expected SR₀ benchmark for N=10, variance=0.2 so the
	// deflated probability is in [0.5, 0.95].
	got := ComputeDSR(
		0.8,  // observedSharpe — moderate
		0.2,  // sharpeVariance
		10,   // nTrials
		50,   // horizonT (~10 weeks of daily bars)
		-0.1, // mild left skew
		0.5,  // moderate excess kurtosis
	)
	if math.IsNaN(got) {
		t.Fatalf("DSR = NaN with valid inputs")
	}
	if got <= 0 || got >= 1 {
		t.Errorf("DSR = %v, want in (0, 1)", got)
	}
}

func TestComputeDSR_NaNOnZeroVariance(t *testing.T) {
	// sharpeVariance ≤ 0 → SR₀ undefined → NaN.
	if got := ComputeDSR(1.5, 0, 10, 365, 0, 0); !math.IsNaN(got) {
		t.Errorf("variance=0: ComputeDSR = %v, want NaN", got)
	}
	if got := ComputeDSR(1.5, -0.1, 10, 365, 0, 0); !math.IsNaN(got) {
		t.Errorf("variance<0: ComputeDSR = %v, want NaN", got)
	}
}

func TestComputeDSR_NaNOnTooShortHorizon(t *testing.T) {
	if got := ComputeDSR(1.5, 0.2, 10, 1, 0, 0); !math.IsNaN(got) {
		t.Errorf("T=1: ComputeDSR = %v, want NaN", got)
	}
}

func TestComputeDSR_NaNOnNegativeRadicand(t *testing.T) {
	// σ_SR radicand = 1 - skew·SR + (excessKurt/4)·SR² can go ≤ 0
	// with extreme inputs. Engineer one:
	// SR=10, skew=+1, excessKurt=0 → 1 - 10 + 0 = -9.
	if got := ComputeDSR(10, 0.2, 10, 365, 1, 0); !math.IsNaN(got) {
		t.Errorf("negative radicand: ComputeDSR = %v, want NaN", got)
	}
}

func TestComputeDSR_HigherObservedSharpeRaisesDSR(t *testing.T) {
	// Monotonicity: with the other inputs fixed, DSR must increase
	// in observedSharpe. Asserts the formula isn't accidentally
	// flipped in sign.
	low := ComputeDSR(0.5, 0.2, 10, 365, 0, 0)
	high := ComputeDSR(2.0, 0.2, 10, 365, 0, 0)
	if !(high > low) {
		t.Errorf("DSR not monotone in SR_obs: low=%v high=%v", low, high)
	}
}
