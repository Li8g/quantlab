package verification

import (
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/resultpkg"
)

// reviewWeights mirrors engine.WindowWeights() (duplicated here to keep
// the verification package free of an engine import, same as the
// production caller passes them in).
func reviewWeights() map[resultpkg.WindowName]float64 {
	return map[resultpkg.WindowName]float64{
		resultpkg.Window6M:  0.10,
		resultpkg.Window2Y:  0.20,
		resultpkg.Window5Y:  0.30,
		resultpkg.Window10Y: 0.40,
	}
}

const reviewLambdaCons = 0.3

// expectedScore mirrors what the stub adapter + AggregateScoreTotal
// produce for a single Window6M result carrying `score`, so the test
// asserts against the real aggregation rather than a hand-computed value.
func expectedScore(t *testing.T, score float64) resultpkg.ScoreTotal {
	t.Helper()
	v := score
	cr := resultpkg.CrucibleResult{
		Window: resultpkg.Window6M,
		Score:  resultpkg.SliceScore{Fatal: false, Value: &v},
	}
	return fitness.AggregateScoreTotal(
		[]resultpkg.CrucibleResult{cr}, reviewWeights(), reviewLambdaCons,
		resultpkg.FitnessVersionV1RawStd,
	)
}

func TestRunReview_OK(t *testing.T) {
	strat := &stubOOSStrategy{score: 0.5}
	plan := planWithOOS(flatBars(10, 0)) // adapter ignores plan windows
	gene := domain.Gene{0}

	expect := ReviewExpectation{
		Score:       expectedScore(t, 0.5),
		Fingerprint: strat.Fingerprint(gene), // stub returns "stub"
		PlanHash:    "plan-h1",
		BarsHash:    "bars-h1",
	}

	got, err := RunReview(strat, plan, gene, expect, "plan-h1", "bars-h1", reviewWeights(), reviewLambdaCons)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusOK {
		t.Errorf("Status = %q, want ok; notes=%v", got.Status, derefNotes(got))
	}
	if got.DataScope == nil || *got.DataScope != "is-windows" {
		t.Errorf("DataScope = %v, want is-windows", got.DataScope)
	}
	if strat.resetCalls != 1 {
		t.Errorf("resetCalls = %d, want 1 (ok path replays once)", strat.resetCalls)
	}
}

func TestRunReview_MismatchScore(t *testing.T) {
	strat := &stubOOSStrategy{score: 0.5}
	plan := planWithOOS(flatBars(10, 0))
	gene := domain.Gene{0}

	// Record a score that the replay (score=0.5) will not reproduce.
	bad := expectedScore(t, 0.5)
	perturbed := *bad.Value + 1.0
	bad.Value = &perturbed

	expect := ReviewExpectation{
		Score:       bad,
		Fingerprint: strat.Fingerprint(gene),
		PlanHash:    "plan-h1",
		BarsHash:    "bars-h1",
	}

	got, err := RunReview(strat, plan, gene, expect, "plan-h1", "bars-h1", reviewWeights(), reviewLambdaCons)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusMismatch {
		t.Errorf("Status = %q, want mismatch", got.Status)
	}
	if strat.resetCalls != 1 {
		t.Errorf("resetCalls = %d, want 1 (score mismatch is found after replay)", strat.resetCalls)
	}
}

func TestRunReview_MismatchHash_NoReplay(t *testing.T) {
	strat := &stubOOSStrategy{score: 0.5}
	plan := planWithOOS(flatBars(10, 0))
	gene := domain.Gene{0}

	expect := ReviewExpectation{
		Score:       expectedScore(t, 0.5),
		Fingerprint: strat.Fingerprint(gene),
		PlanHash:    "plan-h1",
		BarsHash:    "bars-h1",
	}

	// Rebuilt plan_hash drifted → mismatch BEFORE any replay.
	got, err := RunReview(strat, plan, gene, expect, "plan-DRIFTED", "bars-h1", reviewWeights(), reviewLambdaCons)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusMismatch {
		t.Errorf("Status = %q, want mismatch", got.Status)
	}
	if strat.resetCalls != 0 {
		t.Errorf("resetCalls = %d, want 0 (hash gate must short-circuit before replay)", strat.resetCalls)
	}
}

func TestRunReview_MismatchFingerprint_NoReplay(t *testing.T) {
	strat := &stubOOSStrategy{score: 0.5}
	plan := planWithOOS(flatBars(10, 0))
	gene := domain.Gene{0}

	expect := ReviewExpectation{
		Score:       expectedScore(t, 0.5),
		Fingerprint: "recorded-different-fp",
		PlanHash:    "plan-h1",
		BarsHash:    "bars-h1",
	}

	got, err := RunReview(strat, plan, gene, expect, "plan-h1", "bars-h1", reviewWeights(), reviewLambdaCons)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if got.Status != resultpkg.VerificationStatusMismatch {
		t.Errorf("Status = %q, want mismatch", got.Status)
	}
	if strat.resetCalls != 0 {
		t.Errorf("resetCalls = %d, want 0 (fingerprint gate short-circuits before replay)", strat.resetCalls)
	}
}

func derefNotes(r *resultpkg.ReviewSummary) string {
	if r == nil || r.Notes == nil {
		return ""
	}
	return *r.Notes
}
