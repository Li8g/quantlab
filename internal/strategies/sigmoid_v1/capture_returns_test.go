package sigmoid_v1

import (
	"reflect"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// TestEvaluate_CaptureReturnsIsScoreNeutral pins the Phase B (A5)
// determinism contract: plan.CaptureReturns changes ONLY whether the
// longest non-Fatal window's per-bar return series is attached — never
// the scores, window results, or stats. The post-epoch SBB stress
// re-run flips this flag and must not perturb the IS ScoreTotal already
// written during RunEpoch.
func TestEvaluate_CaptureReturnsIsScoreNeutral(t *testing.T) {
	s := windowTestSigmoid()
	bars := flatBars(80, 100, windowTestRefMs)
	gene := stepTestGene()

	planOff := fourWindowPlan(bars, 5, domain.FrictionParams{})
	planOn := *planOff // shallow copy; only CaptureReturns differs
	planOn.CaptureReturns = true

	off := mustAdapterEvaluate(t, s, planOff, gene)
	on := mustAdapterEvaluate(t, s, &planOn, gene)

	// Score-bearing outputs must be identical (DeepEqual follows the
	// *float64 SliceScore.Value pointers).
	if !reflect.DeepEqual(off.Windows, on.Windows) {
		t.Error("CaptureReturns changed Windows (score-bearing) — must be neutral")
	}
	if !reflect.DeepEqual(off.LongestWindowStats, on.LongestWindowStats) {
		t.Error("CaptureReturns changed LongestWindowStats — must be neutral")
	}
	if off.BarsEvaluated != on.BarsEvaluated {
		t.Errorf("CaptureReturns changed BarsEvaluated: %d vs %d",
			off.BarsEvaluated, on.BarsEvaluated)
	}

	// The flag's sole effect: the series attaches only when set.
	if off.LongestWindowReturns != nil {
		t.Errorf("flag off: LongestWindowReturns has %d entries, want nil",
			len(off.LongestWindowReturns))
	}
	if len(on.LongestWindowReturns) == 0 {
		t.Fatal("flag on: LongestWindowReturns empty, want the longest window's series")
	}
	// The attached series IS the input to LongestWindowStats, so their
	// lengths must agree (HorizonT == len(returns)).
	if on.LongestWindowStats != nil &&
		len(on.LongestWindowReturns) != on.LongestWindowStats.HorizonT {
		t.Errorf("series len=%d != LongestWindowStats.HorizonT=%d",
			len(on.LongestWindowReturns), on.LongestWindowStats.HorizonT)
	}
}

func mustAdapterEvaluate(t *testing.T, s *Sigmoid, plan *domain.EvaluablePlan, gene domain.Gene) *resultpkg.RawEvaluateResult {
	t.Helper()
	a, err := s.NewAdapter(plan)
	if err != nil {
		t.Fatalf("NewAdapter: %v", err)
	}
	defer a.Close()
	if err := a.Reset(plan); err != nil {
		t.Fatalf("Reset: %v", err)
	}
	res, err := a.Evaluate(gene)
	if err != nil {
		t.Fatalf("Evaluate: %v", err)
	}
	return res
}
