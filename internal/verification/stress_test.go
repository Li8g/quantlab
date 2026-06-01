package verification

import (
	"context"
	"errors"
	"testing"

	"quantlab/internal/domain"
)

func TestRunStress_ProducesReportFromCapturedReturns(t *testing.T) {
	series := genAR1(0.3, 1000, 100, 0x5EED)
	strat := &stubOOSStrategy{stressReturns: series}
	plan := &domain.EvaluablePlan{FatalMDD: 0.70}

	rep, err := RunStress(context.Background(), strat, plan, domain.Gene{0}, 42)
	if err != nil {
		t.Fatalf("RunStress: %v", err)
	}
	if rep == nil {
		t.Fatal("RunStress returned nil report, want a report from the captured series")
	}
	if !strat.gotCaptureReturns {
		t.Error("RunStress did not set CaptureReturns on the re-run plan")
	}
	if rep.Version != StressSummaryVersionV1 {
		t.Errorf("Version=%q, want %q", rep.Version, StressSummaryVersionV1)
	}
	if rep.NIter != DefaultStressIters {
		t.Errorf("NIter=%d, want %d", rep.NIter, DefaultStressIters)
	}
	// Reproducible: same seed ⇒ identical report.
	rep2, _ := RunStress(context.Background(), strat, plan, domain.Gene{0}, 42)
	if *rep != *rep2 {
		t.Errorf("same seed produced different reports:\n %+v\n %+v", *rep, *rep2)
	}
}

func TestRunStress_NoSeriesIsSkippedNotError(t *testing.T) {
	// stressReturns nil ⇒ adapter attaches nothing ⇒ stress is skipped
	// (nil,nil), never an error — all-Fatal best gene path.
	strat := &stubOOSStrategy{stressReturns: nil}
	plan := &domain.EvaluablePlan{FatalMDD: 0.70}

	rep, err := RunStress(context.Background(), strat, plan, domain.Gene{0}, 1)
	if err != nil {
		t.Fatalf("RunStress: unexpected error %v", err)
	}
	if rep != nil {
		t.Errorf("RunStress=%+v, want nil (no series to bootstrap)", *rep)
	}
}

func TestRunStress_AdapterErrorPropagates(t *testing.T) {
	strat := &stubOOSStrategy{newAdapterErr: errors.New("boom")}
	plan := &domain.EvaluablePlan{FatalMDD: 0.70}

	if _, err := RunStress(context.Background(), strat, plan, domain.Gene{0}, 1); err == nil {
		t.Error("RunStress: want error when NewAdapter fails, got nil")
	}
}
