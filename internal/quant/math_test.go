package quant

import (
	"math"
	"testing"
)

const eps = 1e-9

func approx(a, b, tol float64) bool {
	if math.IsNaN(a) || math.IsNaN(b) {
		return false
	}
	return math.Abs(a-b) <= tol
}

// ----- EMA -----

func TestEMA_Basic(t *testing.T) {
	in := []float64{1, 2, 3, 4, 5}
	out := EMA(in, 3)
	// alpha = 2/4 = 0.5
	// out[0]=1; out[1]=0.5*2+0.5*1=1.5; out[2]=0.5*3+0.5*1.5=2.25;
	// out[3]=0.5*4+0.5*2.25=3.125; out[4]=0.5*5+0.5*3.125=4.0625
	want := []float64{1, 1.5, 2.25, 3.125, 4.0625}
	if len(out) != len(want) {
		t.Fatalf("len: got %d want %d", len(out), len(want))
	}
	for i := range out {
		if !approx(out[i], want[i], eps) {
			t.Errorf("EMA[%d]: got %v want %v", i, out[i], want[i])
		}
	}
}

func TestEMA_Empty(t *testing.T) {
	if got := EMA(nil, 3); len(got) != 0 {
		t.Errorf("EMA(nil) = %v, want []", got)
	}
}

func TestEMA_PanicOnZeroPeriod(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	EMA([]float64{1, 2}, 0)
}

// ----- StdDev -----

func TestStdDev_Basic(t *testing.T) {
	// Textbook sample {2,4,4,4,5,5,7,9}: Σ(x-μ)² = 32, n = 8.
	// Sample stddev (n-1 denom, what StdDev uses) = √(32/7) ≈ 2.138089935299395.
	// Population stddev would be 2.0 — we deliberately use sample here.
	in := []float64{2, 4, 4, 4, 5, 5, 7, 9}
	got := StdDev(in)
	want := math.Sqrt(32.0 / 7.0)
	if !approx(got, want, 1e-12) {
		t.Errorf("StdDev = %v, want %v", got, want)
	}
}

func TestStdDev_DegenerateInputs(t *testing.T) {
	if got := StdDev(nil); got != 0 {
		t.Errorf("StdDev(nil) = %v, want 0", got)
	}
	if got := StdDev([]float64{42}); got != 0 {
		t.Errorf("StdDev([42]) = %v, want 0", got)
	}
}

// ----- MAVAbsChange -----

func TestMAVAbsChange_Basic(t *testing.T) {
	// |2-1|, |1-2|, |4-1|, |0-4| = 1, 1, 3, 4 -> mean = 9/4 = 2.25
	got := MAVAbsChange([]float64{1, 2, 1, 4, 0})
	if !approx(got, 2.25, eps) {
		t.Errorf("MAVAbsChange = %v, want 2.25", got)
	}
}

func TestMAVAbsChange_ShortInput(t *testing.T) {
	if got := MAVAbsChange([]float64{1}); got != 0 {
		t.Errorf("MAVAbsChange([1]) = %v, want 0", got)
	}
}

// ----- MAVAbsChangeWindow -----

func TestMAVAbsChangeWindow_Basic(t *testing.T) {
	// series 1,2,1,4,0 — diffs |1|,|1|,|3|,|4|
	// window=4 -> mean of all 4 diffs = 2.25
	if got := MAVAbsChangeWindow([]float64{1, 2, 1, 4, 0}, 4); !approx(got, 2.25, eps) {
		t.Errorf("window=4 got %v, want 2.25", got)
	}
	// window=2 -> trailing two diffs |3|,|4| -> 3.5
	if got := MAVAbsChangeWindow([]float64{1, 2, 1, 4, 0}, 2); !approx(got, 3.5, eps) {
		t.Errorf("window=2 got %v, want 3.5", got)
	}
}

func TestMAVAbsChangeWindow_TooShort(t *testing.T) {
	// window+1 > n -> 0
	if got := MAVAbsChangeWindow([]float64{1, 2, 3}, 5); got != 0 {
		t.Errorf("len=3 window=5 got %v, want 0", got)
	}
	// n < 2 -> 0
	if got := MAVAbsChangeWindow([]float64{42}, 1); got != 0 {
		t.Errorf("len=1 got %v, want 0", got)
	}
	// window<=0 -> 0
	if got := MAVAbsChangeWindow([]float64{1, 2, 3}, 0); got != 0 {
		t.Errorf("window=0 got %v, want 0", got)
	}
}

func TestMAVAbsChangeWindow_ConstantSeries(t *testing.T) {
	// All diffs zero -> mean zero.
	if got := MAVAbsChangeWindow([]float64{7, 7, 7, 7, 7}, 4); got != 0 {
		t.Errorf("constant series got %v, want 0", got)
	}
}

// ----- ClipFloat64 -----

func TestClipFloat64(t *testing.T) {
	cases := []struct {
		x, lo, hi, want float64
	}{
		{5, 0, 10, 5},
		{-1, 0, 10, 0},
		{11, 0, 10, 10},
		{0, 0, 0, 0},
	}
	for _, c := range cases {
		got := ClipFloat64(c.x, c.lo, c.hi)
		if got != c.want {
			t.Errorf("Clip(%v,%v,%v) = %v, want %v", c.x, c.lo, c.hi, got, c.want)
		}
	}
}

func TestClipFloat64_PanicLoGtHi(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	ClipFloat64(0, 10, 5)
}

// ----- RoundToUSDT -----

func TestRoundToUSDT(t *testing.T) {
	// Inputs chosen so the multiplication x*1e8 lands cleanly on an
	// integer boundary or a clear half — avoids float64-literal precision
	// noise (e.g. 1.123456785 stores as 1.1234567849999...).
	cases := []struct{ in, want float64 }{
		{1.12345678, 1.12345678},      // already at precision
		{1.123456784, 1.12345678},     // rounds down
		{1.123456786, 1.12345679},     // rounds up
		{0, 0},
		{-1.123456784, -1.12345678},
		{-1.123456786, -1.12345679},
	}
	for _, c := range cases {
		got := RoundToUSDT(c.in)
		if !approx(got, c.want, 1e-12) {
			t.Errorf("RoundToUSDT(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}

// ----- ACF -----

func TestACF_LagZeroIsOne(t *testing.T) {
	in := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	out := ACF(in, 3)
	if len(out) != 4 {
		t.Fatalf("len = %d, want 4", len(out))
	}
	if !approx(out[0], 1.0, eps) {
		t.Errorf("ACF[0] = %v, want 1.0", out[0])
	}
}

func TestACF_ConstantSeries(t *testing.T) {
	in := []float64{5, 5, 5, 5, 5}
	out := ACF(in, 2)
	if out[0] != 1 || out[1] != 0 || out[2] != 0 {
		t.Errorf("ACF(constant) = %v, want [1,0,0]", out)
	}
}

func TestACF_KnownLinearTrend(t *testing.T) {
	// For arithmetic progression {1..N}, ρ(1) ≈ 1 - 3/N for large N.
	// Just sanity-check it's positive and decreasing.
	in := make([]float64, 100)
	for i := range in {
		in[i] = float64(i + 1)
	}
	out := ACF(in, 5)
	if out[1] <= 0 || out[1] > 1 {
		t.Errorf("ACF[1] = %v, expected (0,1]", out[1])
	}
	for k := 2; k <= 5; k++ {
		if out[k] >= out[k-1] {
			t.Errorf("ACF not decreasing at lag %d: %v >= %v", k, out[k], out[k-1])
		}
	}
}

func TestACF_PanicOnTooLargeLag(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Fatal("expected panic")
		}
	}()
	ACF([]float64{1, 2, 3}, 5)
}

// ----- Skewness -----

func TestSkewness_SymmetricIsZero(t *testing.T) {
	in := []float64{-2, -1, 0, 1, 2}
	got := Skewness(in)
	if !approx(got, 0, 1e-12) {
		t.Errorf("Skewness(symmetric) = %v, want 0", got)
	}
}

func TestSkewness_RightSkewedIsPositive(t *testing.T) {
	// Right-skewed: long right tail.
	in := []float64{1, 1, 1, 1, 1, 2, 2, 3, 5, 10}
	got := Skewness(in)
	if got <= 0 {
		t.Errorf("Skewness(right-skewed) = %v, want > 0", got)
	}
}

func TestSkewness_ShortInput(t *testing.T) {
	if got := Skewness([]float64{1, 2}); got != 0 {
		t.Errorf("Skewness(n<3) = %v, want 0", got)
	}
}

// ----- ExcessKurtosis -----

func TestExcessKurtosis_NormalIsApproxZero(t *testing.T) {
	// Box-Muller pairs from a fixed seed would be cleaner, but a
	// large near-Gaussian sample suffices: excess kurtosis should be
	// close to 0. Use a deterministic linear set with mild noise — its
	// excess kurtosis is roughly that of a uniform (-1.2), so we just
	// check that the function returns a finite, reasonable value.
	in := make([]float64, 1000)
	for i := range in {
		in[i] = float64(i)
	}
	got := ExcessKurtosis(in)
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("ExcessKurtosis = %v, want finite", got)
	}
	// Uniform distribution has excess kurtosis = -6/5 = -1.2.
	if !approx(got, -1.2, 0.01) {
		t.Errorf("ExcessKurtosis(uniform) = %v, want approx -1.2", got)
	}
}

func TestExcessKurtosis_ShortInput(t *testing.T) {
	if got := ExcessKurtosis([]float64{1, 2, 3}); got != 0 {
		t.Errorf("ExcessKurtosis(n<4) = %v, want 0", got)
	}
}

// ----- KahanSum -----

func TestKahanSum_MatchesNaiveForExactValues(t *testing.T) {
	in := []float64{1, 2, 3, 4, 5}
	if got := KahanSum(in); got != 15 {
		t.Errorf("KahanSum = %v, want 15", got)
	}
}

func TestKahanSum_StableForLongSeries(t *testing.T) {
	// Classic Kahan test: 1e10 summed with many small terms.
	in := []float64{1e10}
	for i := 0; i < 100000; i++ {
		in = append(in, 1e-3)
	}
	// True sum = 1e10 + 100. Naive float64 may lose ~the trailing precision.
	got := KahanSum(in)
	want := 1e10 + 100
	if math.Abs(got-want) > 1e-3 {
		t.Errorf("KahanSum drift: got %v want %v", got, want)
	}
}

func TestKahanSum_Empty(t *testing.T) {
	if got := KahanSum(nil); got != 0 {
		t.Errorf("KahanSum(nil) = %v, want 0", got)
	}
}
