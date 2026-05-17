package fitness

import (
	"math"
	"testing"

	"quantlab/internal/resultpkg"
)

func defaultWeights() map[resultpkg.WindowName]float64 {
	return map[resultpkg.WindowName]float64{
		resultpkg.Window6M:  0.10,
		resultpkg.Window2Y:  0.20,
		resultpkg.Window5Y:  0.30,
		resultpkg.Window10Y: 0.40,
	}
}

func normalWindow(name resultpkg.WindowName, v float64) resultpkg.CrucibleResult {
	return resultpkg.CrucibleResult{
		Window: name,
		Score:  resultpkg.SliceScore{Fatal: false, Value: &v},
	}
}

func fatalWindow(name resultpkg.WindowName) resultpkg.CrucibleResult {
	return resultpkg.CrucibleResult{
		Window: name,
		Score:  resultpkg.SliceScore{Fatal: true},
	}
}

func skippedWindow(name resultpkg.WindowName, cause resultpkg.SkippedBy) resultpkg.CrucibleResult {
	c := cause
	return resultpkg.CrucibleResult{
		Window:    name,
		Score:     resultpkg.SliceScore{Fatal: false}, // Value=nil
		SkippedBy: &c,
	}
}

func TestAggregate_AllNormalEqualScores(t *testing.T) {
	// All four windows score = 1.0 → scoreRaw = 1.0 * (0.1+0.2+0.3+0.4) = 1.0
	// σ = 0 (no variance) → Value = 1.0.
	windows := []resultpkg.CrucibleResult{
		normalWindow(resultpkg.Window6M, 1.0),
		normalWindow(resultpkg.Window2Y, 1.0),
		normalWindow(resultpkg.Window5Y, 1.0),
		normalWindow(resultpkg.Window10Y, 1.0),
	}
	got := AggregateScoreTotal(windows, defaultWeights(), 0.3, resultpkg.FitnessVersionV1RawStd)
	if got.Fatal {
		t.Fatal("Fatal=true on all-normal input")
	}
	if math.Abs(*got.Value-1.0) > 1e-12 {
		t.Errorf("Value = %g, want 1.0", *got.Value)
	}
	if math.Abs(*got.ConsistencyPenalty) > 1e-12 {
		t.Errorf("σ = %g, want 0", *got.ConsistencyPenalty)
	}
}

func TestAggregate_FatalPropagates(t *testing.T) {
	// One fatal → ScoreTotal.Fatal=true, Value=nil.
	windows := []resultpkg.CrucibleResult{
		normalWindow(resultpkg.Window6M, 0.5),
		fatalWindow(resultpkg.Window2Y),
		normalWindow(resultpkg.Window5Y, 0.5),
	}
	got := AggregateScoreTotal(windows, defaultWeights(), 0.3, resultpkg.FitnessVersionV1RawStd)
	if !got.Fatal {
		t.Error("Fatal not propagated")
	}
	if got.Value != nil {
		t.Errorf("Value = %v, want nil for Fatal", got.Value)
	}
}

func TestAggregate_CascadeSkippedDropped(t *testing.T) {
	// 6m = 1.0, others cascade-skipped → scoreRaw = 0.1; σ undefined → 0.
	windows := []resultpkg.CrucibleResult{
		normalWindow(resultpkg.Window6M, 1.0),
		skippedWindow(resultpkg.Window2Y, resultpkg.SkippedByCascadeFrom6M),
		skippedWindow(resultpkg.Window5Y, resultpkg.SkippedByCascadeFrom6M),
		skippedWindow(resultpkg.Window10Y, resultpkg.SkippedByCascadeFrom6M),
	}
	got := AggregateScoreTotal(windows, defaultWeights(), 0.3, resultpkg.FitnessVersionV1RawStd)
	if got.Fatal {
		t.Fatal("Fatal=true with no fatal windows")
	}
	if math.Abs(*got.ScoreRaw-0.1) > 1e-12 {
		t.Errorf("ScoreRaw = %g, want 0.1 (no renormalization)", *got.ScoreRaw)
	}
	if *got.ConsistencyPenalty != 0 {
		t.Errorf("σ = %g, want 0 for single-window", *got.ConsistencyPenalty)
	}
}

func TestAggregate_ConsistencyPenaltyReducesValue(t *testing.T) {
	// Mixed scores → nonzero σ → Value < ScoreRaw.
	windows := []resultpkg.CrucibleResult{
		normalWindow(resultpkg.Window6M, 0.0),
		normalWindow(resultpkg.Window2Y, 1.0),
		normalWindow(resultpkg.Window5Y, 0.0),
		normalWindow(resultpkg.Window10Y, 1.0),
	}
	got := AggregateScoreTotal(windows, defaultWeights(), 0.3, resultpkg.FitnessVersionV1RawStd)
	if *got.ConsistencyPenalty <= 0 {
		t.Errorf("σ = %g, want > 0 for mixed scores", *got.ConsistencyPenalty)
	}
	if *got.Value >= *got.ScoreRaw {
		t.Errorf("Value (%g) should be less than ScoreRaw (%g) when σ > 0",
			*got.Value, *got.ScoreRaw)
	}
}

func TestAggregate_WeightsNotRenormalized(t *testing.T) {
	// Drop two windows (no entry at all — not even Skipped). Expect that
	// the remaining 6m and 2y contribute their original weights (0.1 + 0.2),
	// NOT a renormalized share. score = 1.0 for both → scoreRaw = 0.3.
	windows := []resultpkg.CrucibleResult{
		normalWindow(resultpkg.Window6M, 1.0),
		normalWindow(resultpkg.Window2Y, 1.0),
	}
	got := AggregateScoreTotal(windows, defaultWeights(), 0.3, resultpkg.FitnessVersionV1RawStd)
	if math.Abs(*got.ScoreRaw-0.3) > 1e-12 {
		t.Errorf("ScoreRaw = %g, want 0.3 (sum of original weights, no renorm)", *got.ScoreRaw)
	}
}

func TestAggregate_PopulationStdDevFormula(t *testing.T) {
	// σ over {0, 1} with N denominator: mean=0.5, var=((0.25+0.25)/2)=0.25, σ=0.5.
	// Use λ_cons=1 and weights that produce ScoreRaw=0.5 for easy assert.
	weights := map[resultpkg.WindowName]float64{
		resultpkg.Window6M: 0.5,
		resultpkg.Window2Y: 0.5,
	}
	windows := []resultpkg.CrucibleResult{
		normalWindow(resultpkg.Window6M, 0.0),
		normalWindow(resultpkg.Window2Y, 1.0),
	}
	got := AggregateScoreTotal(windows, weights, 1.0, resultpkg.FitnessVersionV1RawStd)
	if math.Abs(*got.ConsistencyPenalty-0.5) > 1e-12 {
		t.Errorf("σ = %g, want 0.5", *got.ConsistencyPenalty)
	}
	// ScoreRaw = 0.5*0 + 0.5*1 = 0.5; Value = 0.5 - 1.0*0.5 = 0.
	if math.Abs(*got.Value) > 1e-12 {
		t.Errorf("Value = %g, want 0", *got.Value)
	}
}
