package sigmoid_v1

import (
	"math"
	"testing"
)

// signalTestChromosome returns the §4.1 defaults with overridable bits
// for table tests. Period choices keep emaLong reachable on short
// fixture series.
func signalTestChromosome() Chromosome {
	c := defaultChromosome()
	// Shrink long-period dimensions so 40-bar fixtures suffice. Validate
	// still passes because short < long is preserved.
	c.EMALongPeriod = 10
	c.EMAShortPeriod = 5
	c.MAVLongPeriod = 8
	c.MAVShortPeriod = 4
	return c
}

func TestComputeSignal_ZeroWeights(t *testing.T) {
	c := signalTestChromosome()
	c.A1, c.A2, c.A3 = 0, 0, 0
	closes := []float64{100, 101, 102, 103, 104, 105, 106, 107, 108, 109, 110, 111, 112, 113, 114, 115}
	if got := ComputeSignal(closes, c, 1.5); got != 0 {
		t.Errorf("zero weights → %v, want 0", got)
	}
}

func TestComputeSignal_VolRatioCentredOnly(t *testing.T) {
	// Constant prices: priceDeviation = 0 (close == EMA == 100) and
	// logReturn = ln(100/100) = 0. Only the volRatio-centred term
	// contributes.
	c := signalTestChromosome()
	c.A1, c.A2 = 0, 0
	c.A3 = 0.5
	closes := flatCloses(16, 100.0)
	got := ComputeSignal(closes, c, 2.0)
	want := 0.5 * (2.0 - 1.0)
	if math.Abs(got-want) > 1e-12 {
		t.Errorf("flat closes vol=2 → %v, want %v", got, want)
	}
}

func TestComputeSignal_LogReturnSign(t *testing.T) {
	// Monotonic ascent → logReturn > 0; A2 > 0 → positive signal.
	c := signalTestChromosome()
	c.A1, c.A3 = 0, 0
	c.A2 = 1.0
	// 16 bars, price doubling overall; the MAVShort=4 lookback sees
	// closes[15]=2*closes[11], so logReturn = ln(2) > 0.
	closes := make([]float64, 16)
	closes[0] = 100
	for i := 1; i < 16; i++ {
		closes[i] = closes[i-1] * math.Pow(2, 1.0/15.0)
	}
	got := ComputeSignal(closes, c, 1.0)
	if got <= 0 {
		t.Errorf("monotone ascent → %v, want > 0", got)
	}
	// Mirror: monotonic descent → negative.
	descend := make([]float64, 16)
	descend[0] = 100
	for i := 1; i < 16; i++ {
		descend[i] = descend[i-1] / math.Pow(2, 1.0/15.0)
	}
	if got := ComputeSignal(descend, c, 1.0); got >= 0 {
		t.Errorf("monotone descent → %v, want < 0", got)
	}
}

func TestComputeSignal_PriceDeviationSign(t *testing.T) {
	// Force priceDeviation > 0 by making the final close jump well
	// above the long-EMA. Other terms are silenced.
	c := signalTestChromosome()
	c.A2, c.A3 = 0, 0
	c.A1 = 1.0
	closes := flatCloses(15, 100.0)
	closes = append(closes, 200.0) // single late spike
	got := ComputeSignal(closes, c, 1.0)
	if got <= 0 {
		t.Errorf("late spike → %v, want > 0 (close >> EMA)", got)
	}
}

func TestComputeSignal_TooFewBars(t *testing.T) {
	c := signalTestChromosome()
	if got := ComputeSignal([]float64{100}, c, 1.0); got != 0 {
		t.Errorf("len=1 → %v, want 0", got)
	}
	if got := ComputeSignal(nil, c, 1.0); got != 0 {
		t.Errorf("nil → %v, want 0", got)
	}
	// len <= MAVShortPeriod (4) cannot form the logReturn lookback.
	short := []float64{100, 100, 100, 100}
	if got := ComputeSignal(short, c, 1.0); got != 0 {
		t.Errorf("len <= MAVShort → %v, want 0", got)
	}
}

func TestComputeSignal_NoNaN(t *testing.T) {
	// All-zero closes — pathological, but the function should not
	// emit NaN/Inf. logReturn is skipped via the prev>0 guard,
	// priceDeviation is skipped via the emaLong != 0 guard.
	c := signalTestChromosome()
	c.A1, c.A2, c.A3 = 1.0, 1.0, 1.0
	closes := make([]float64, 16) // zeros
	got := ComputeSignal(closes, c, 1.0)
	if math.IsNaN(got) || math.IsInf(got, 0) {
		t.Errorf("all-zero closes → non-finite %v", got)
	}
}

// ----- computeMicroRebalance -----

func TestMicroRebalance_AllUSDT_CurrentWeightZero(t *testing.T) {
	c := signalTestChromosome()
	r := computeMicroRebalance(c, microRebalanceInputs{
		USDT: 1000, Price: 50_000, MarketBetaMul: 1.0, Signal: 0,
	})
	if r.CurrentWeight != 0 {
		t.Errorf("USDT-only: currentWeight=%v, want 0", r.CurrentWeight)
	}
	if r.TotalEquity != 1000 {
		t.Errorf("USDT-only: totalEquity=%v, want 1000", r.TotalEquity)
	}
}

func TestMicroRebalance_NeutralSignal_HalfWeight(t *testing.T) {
	// signal=0, gamma=0, currentWeight = 0.5 (FloatBTC*price == USDT
	// half-and-half). exponent = 0 → target = 0.5 → delta = 0.
	c := signalTestChromosome()
	c.Gamma = 0
	r := computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.01, USDT: 500, Price: 50_000, // each side = 500
		MarketBetaMul: 1.0, Signal: 0,
	})
	if math.Abs(r.CurrentWeight-0.5) > 1e-9 {
		t.Errorf("currentWeight=%v, want 0.5", r.CurrentWeight)
	}
	if math.Abs(r.TargetWeight-0.5) > 1e-9 {
		t.Errorf("targetWeight=%v, want 0.5", r.TargetWeight)
	}
	if math.Abs(r.DeltaWeight) > 1e-9 {
		t.Errorf("deltaWeight=%v, want 0", r.DeltaWeight)
	}
}

func TestMicroRebalance_PositiveSignalReducesTarget(t *testing.T) {
	// Positive signal → exponent > 0 → 1/(1+exp(+)) < 0.5 → sell.
	c := signalTestChromosome()
	c.Gamma = 0
	r := computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.01, USDT: 500, Price: 50_000,
		MarketBetaMul: 1.0, Signal: 0.5,
	})
	if r.TargetWeight >= 0.5 {
		t.Errorf("positive signal: targetWeight=%v, want < 0.5", r.TargetWeight)
	}
	if r.DeltaWeight >= 0 || r.TheoreticalUSD >= 0 {
		t.Errorf("positive signal: delta=%v usd=%v, both want < 0",
			r.DeltaWeight, r.TheoreticalUSD)
	}
}

func TestMicroRebalance_NegativeSignalRaisesTarget(t *testing.T) {
	c := signalTestChromosome()
	c.Gamma = 0
	r := computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.01, USDT: 500, Price: 50_000,
		MarketBetaMul: 1.0, Signal: -0.5,
	})
	if r.TargetWeight <= 0.5 {
		t.Errorf("negative signal: targetWeight=%v, want > 0.5", r.TargetWeight)
	}
	if r.DeltaWeight <= 0 || r.TheoreticalUSD <= 0 {
		t.Errorf("negative signal: delta=%v usd=%v, both want > 0",
			r.DeltaWeight, r.TheoreticalUSD)
	}
}

func TestMicroRebalance_TargetClippedTo01(t *testing.T) {
	// Extreme positive signal × max effective β → exp blow-up →
	// targetWeight folds to 0 cleanly (no NaN, no negative).
	c := signalTestChromosome()
	c.Beta = 5.0
	r := computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.01, USDT: 500, Price: 50_000,
		MarketBetaMul: 1.0, Signal: 1e6,
	})
	if r.TargetWeight != 0 {
		t.Errorf("huge +signal: targetWeight=%v, want 0", r.TargetWeight)
	}
	// Mirror: large negative → folds to 1.
	r = computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.01, USDT: 500, Price: 50_000,
		MarketBetaMul: 1.0, Signal: -1e6,
	})
	if r.TargetWeight != 1 {
		t.Errorf("huge -signal: targetWeight=%v, want 1", r.TargetWeight)
	}
}

func TestMicroRebalance_EffectiveBetaFloor(t *testing.T) {
	// β = 0.5 (min), marketBetaMul = 0.0 (synthetic) → product = 0 →
	// must be floored to 0.01 so exponent is non-degenerate.
	c := signalTestChromosome()
	c.Beta = 0.5
	r := computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.01, USDT: 500, Price: 50_000,
		MarketBetaMul: 0.0, Signal: 0.5,
	})
	if r.EffectiveBeta != effectiveBetaFloor {
		t.Errorf("β·mul=0: effective=%v, want %v", r.EffectiveBeta, effectiveBetaFloor)
	}
}

func TestMicroRebalance_TotalEquityZero(t *testing.T) {
	// Cold-start: nothing in any wallet. Must produce a finite,
	// well-defined currentWeight (= 0) rather than NaN.
	c := signalTestChromosome()
	r := computeMicroRebalance(c, microRebalanceInputs{
		MarketBetaMul: 1.0, Signal: 0,
	})
	if r.TotalEquity != 0 {
		t.Errorf("cold start: totalEquity=%v, want 0", r.TotalEquity)
	}
	if math.IsNaN(r.CurrentWeight) || math.IsInf(r.CurrentWeight, 0) {
		t.Errorf("cold start: currentWeight=%v, want finite", r.CurrentWeight)
	}
	if r.CurrentWeight != 0 {
		t.Errorf("cold start: currentWeight=%v, want 0", r.CurrentWeight)
	}
}

func TestMicroRebalance_DeltaIsTargetMinusCurrent(t *testing.T) {
	// Algebraic invariant: any inputs, delta == target - current.
	c := signalTestChromosome()
	r := computeMicroRebalance(c, microRebalanceInputs{
		FloatBTC: 0.005, USDT: 250, Price: 50_000,
		MarketBetaMul: 0.7, Signal: 0.2,
	})
	if math.Abs(r.DeltaWeight-(r.TargetWeight-r.CurrentWeight)) > 1e-12 {
		t.Errorf("delta=%v but target-current=%v",
			r.DeltaWeight, r.TargetWeight-r.CurrentWeight)
	}
	if math.Abs(r.TheoreticalUSD-r.DeltaWeight*r.TotalEquity) > 1e-9 {
		t.Errorf("theoreticalUSD=%v but delta*equity=%v",
			r.TheoreticalUSD, r.DeltaWeight*r.TotalEquity)
	}
}
