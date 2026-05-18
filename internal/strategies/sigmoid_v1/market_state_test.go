package sigmoid_v1

import (
	"math"
	"testing"
)

// flatCloses returns a slice of `n` identical prices. Useful for the
// degenerate-MAV branch.
func flatCloses(n int, p float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = p
	}
	return out
}

// alternatingCloses returns n bars oscillating between low and high.
// MAV over any window ≥ 1 is |high - low|; volRatio = 1.0.
func alternatingCloses(n int, low, high float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		if i%2 == 0 {
			out[i] = low
		} else {
			out[i] = high
		}
	}
	return out
}

func TestComputeMarketState_FlatLongMAVIsQuiet(t *testing.T) {
	// All-flat history → MAV_long = 0 → degenerate fallback path.
	closes := flatCloses(20, 100.0)
	state, vol := ComputeMarketState(closes, 4, 10, 0.7)
	if state != MarketStateQuiet {
		t.Errorf("flat history: state = %q, want quiet", state)
	}
	if vol != 1.0 {
		t.Errorf("flat history: volRatio = %v, want 1.0 (neutral)", vol)
	}
}

func TestComputeMarketState_BalancedRatioCompareToThreshold(t *testing.T) {
	// Alternating low/high → every c2c diff is |high-low|, so
	// MAV_short == MAV_long → volRatio = 1.0 exactly.
	closes := alternatingCloses(30, 100, 101)
	// threshold 1.5 > 1.0 → quiet.
	if state, vol := ComputeMarketState(closes, 4, 10, 1.5); state != MarketStateQuiet || vol != 1.0 {
		t.Errorf("threshold 1.5: state=%q vol=%v, want (quiet, 1.0)", state, vol)
	}
	// threshold 1.0 == 1.0 → active (strict `<`).
	if state, _ := ComputeMarketState(closes, 4, 10, 1.0); state != MarketStateActive {
		t.Errorf("threshold == ratio: state=%q, want active (strict `<`)", state)
	}
	// threshold 0.5 < 1.0 → active.
	if state, _ := ComputeMarketState(closes, 4, 10, 0.5); state != MarketStateActive {
		t.Errorf("threshold 0.5: state=%q, want active", state)
	}
}

func TestComputeMarketState_VolRatioClippedHigh(t *testing.T) {
	// Build a series whose short MAV is much larger than long MAV.
	// First half: flat, then a fat tail of alternating large swings.
	// Long window (12) covers mostly flat; short window (3) covers
	// only the swings → mavShort/mavLong >> 3.0 → clipped to 3.0.
	closes := flatCloses(10, 100.0)
	closes = append(closes, 100, 200, 100, 200) // huge late swings
	state, vol := ComputeMarketState(closes, 3, 12, 0.7)
	if state != MarketStateActive {
		t.Errorf("late spike: state = %q, want active", state)
	}
	if vol != volRatioMax {
		t.Errorf("late spike: volRatio = %v, want clipped %v", vol, volRatioMax)
	}
}

func TestComputeMarketState_VolRatioClippedLow(t *testing.T) {
	// Inverse: noisy historic tail, recent calm. Short MAV → 0,
	// long MAV >> 0, ratio → 0, clipped to volRatioMin.
	closes := []float64{100, 200, 100, 200, 100, 200, 100, 200, 100, 200}
	// Append 6 flat bars so the trailing short window sees no change.
	closes = append(closes, 200, 200, 200, 200, 200, 200)
	state, vol := ComputeMarketState(closes, 3, 12, 0.7)
	// volRatio clipped to 0.1, which is < 0.7 → quiet.
	if state != MarketStateQuiet {
		t.Errorf("recent calm: state = %q, want quiet", state)
	}
	if vol != volRatioMin {
		t.Errorf("recent calm: volRatio = %v, want clipped %v", vol, volRatioMin)
	}
}

func TestComputeMarketState_DeterministicFromCloses(t *testing.T) {
	// Iron-rule #1: state must be recomputable from the same inputs.
	// We don't pass RuntimeState — the function signature already
	// enforces statelessness — but verify equal inputs give equal
	// outputs across two calls.
	closes := alternatingCloses(40, 50, 75)
	s1, v1 := ComputeMarketState(closes, 5, 15, 0.7)
	s2, v2 := ComputeMarketState(closes, 5, 15, 0.7)
	if s1 != s2 || v1 != v2 {
		t.Errorf("nondeterministic: (%v, %v) vs (%v, %v)", s1, v1, s2, v2)
	}
}

func TestMarketBetaMultiplier(t *testing.T) {
	if got := MarketBetaMultiplier(MarketStateQuiet); got != 0.3 {
		t.Errorf("quiet → %v, want 0.3", got)
	}
	if got := MarketBetaMultiplier(MarketStateActive); got != 1.0 {
		t.Errorf("active → %v, want 1.0", got)
	}
	// Any unknown state returned by future code paths should fall
	// through to the active multiplier — guard against silent quiet.
	if got := MarketBetaMultiplier(MarketState("invented")); got != 1.0 {
		t.Errorf("unknown state → %v, want 1.0 fall-through", got)
	}
}

func TestComputeMarketState_NoNaN(t *testing.T) {
	// Defense-in-depth: even on the degenerate flat path the returned
	// volRatio must be a regular float (DebugSnapshot would otherwise
	// emit "NaN" through json.Marshal and fail downstream tests).
	_, vol := ComputeMarketState(flatCloses(15, 100), 3, 10, 0.7)
	if math.IsNaN(vol) || math.IsInf(vol, 0) {
		t.Errorf("flat history produced non-finite volRatio=%v", vol)
	}
}
