package verification

import (
	"math"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// crucibleWindow builds an n-bar flat-price (=100) window with the given
// name and warmup. dayMs spacing matches flatBars.
func crucibleWindow(name resultpkg.WindowName, n, warmup int) domain.CrucibleWindow {
	bars := flatBars(n, 0)
	return domain.CrucibleWindow{
		Name:      name,
		WarmupLen: warmup,
		Bars:      bars,
		StartTS:   bars[0].OpenTime,
		EndTS:     bars[len(bars)-1].OpenTime,
	}
}

func normalScore(logRet float64) resultpkg.SliceScore {
	return resultpkg.SliceScore{Fatal: false, Value: &logRet}
}

// findWindow returns the WindowAlpha for w, or nil if absent.
func findWindow(ab resultpkg.ISAlphaBreakdown, w resultpkg.WindowName) *resultpkg.WindowAlpha {
	for i := range ab.Windows {
		if ab.Windows[i].Window == w {
			return &ab.Windows[i]
		}
	}
	return nil
}

func TestRunISAlphaBreakdown_NormalWindow(t *testing.T) {
	const n = 366 // ~365 days eval span at warmup=0
	plan := &domain.EvaluablePlan{
		Windows:  []domain.CrucibleWindow{crucibleWindow(resultpkg.Window2Y, n, 0)},
		Friction: domain.FrictionParams{}, // zero friction → clean DCA
	}
	logRet := math.Log(1.1) // +10% total over the span
	windows := []resultpkg.CrucibleResult{
		{Window: resultpkg.Window2Y, Score: normalScore(logRet)},
	}

	ab := RunISAlphaBreakdown(windows, plan, defaultDCA())

	if ab.Version != resultpkg.AlphaBreakdownVersionV1 {
		t.Errorf("Version = %q, want %q", ab.Version, resultpkg.AlphaBreakdownVersionV1)
	}
	wa := findWindow(ab, resultpkg.Window2Y)
	if wa == nil {
		t.Fatalf("Window2Y missing from breakdown: %+v", ab.Windows)
	}

	// Reproduce the annualization to assert strat_ann exactly.
	evalBars := plan.Windows[0].Bars
	years := float64(evalBars[len(evalBars)-1].OpenTime-evalBars[0].OpenTime) / (yearDays * float64(dayMs))
	wantStrat := math.Exp(logRet/years) - 1
	if math.Abs(wa.StratAnn-wantStrat) > 1e-9 {
		t.Errorf("StratAnn = %g, want %g", wa.StratAnn, wantStrat)
	}
	// Flat price + no monthly inject → DCA just holds, ROI≈0 → alpha≈strat.
	if math.Abs(wa.DCAMonthlyAnn) > 1e-9 || math.Abs(wa.DCAWeeklyAnn) > 1e-9 {
		t.Errorf("flat DCA ann should be ~0, got monthly=%g weekly=%g", wa.DCAMonthlyAnn, wa.DCAWeeklyAnn)
	}
	if math.Abs(wa.AlphaMonthlyAnn-wantStrat) > 1e-9 || math.Abs(wa.AlphaWeeklyAnn-wantStrat) > 1e-9 {
		t.Errorf("alpha should ≈ strat_ann=%g, got monthly=%g weekly=%g", wantStrat, wa.AlphaMonthlyAnn, wa.AlphaWeeklyAnn)
	}
}

func TestRunISAlphaBreakdown_OmitsNonNormalAndUnknown(t *testing.T) {
	skip := resultpkg.SkippedByCascadeFrom6M
	plan := &domain.EvaluablePlan{
		Windows: []domain.CrucibleWindow{
			crucibleWindow(resultpkg.Window6M, 200, 0),
			crucibleWindow(resultpkg.Window2Y, 366, 0),
			crucibleWindow(resultpkg.Window5Y, 366, 0),
			// Window10Y deliberately absent from plan.
		},
		Friction: domain.FrictionParams{},
	}
	lr := math.Log(1.05)
	windows := []resultpkg.CrucibleResult{
		{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Fatal: true, Value: nil}},                    // fatal → omit
		{Window: resultpkg.Window2Y, Score: resultpkg.SliceScore{Fatal: false, Value: nil}, SkippedBy: &skip}, // skipped → omit
		{Window: resultpkg.Window5Y, Score: normalScore(lr)},                                                  // normal → present
		{Window: resultpkg.Window10Y, Score: normalScore(lr)},                                                 // not in plan → omit
	}

	ab := RunISAlphaBreakdown(windows, plan, defaultDCA())

	if len(ab.Windows) != 1 {
		t.Fatalf("expected exactly 1 window (5y), got %d: %+v", len(ab.Windows), ab.Windows)
	}
	if ab.Windows[0].Window != resultpkg.Window5Y {
		t.Errorf("present window = %q, want 5y", ab.Windows[0].Window)
	}
}

func TestRunISAlphaBreakdown_AllFatalEmpty(t *testing.T) {
	plan := &domain.EvaluablePlan{
		Windows:  []domain.CrucibleWindow{crucibleWindow(resultpkg.Window6M, 200, 0)},
		Friction: domain.FrictionParams{},
	}
	windows := []resultpkg.CrucibleResult{
		{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Fatal: true, Value: nil}},
	}

	ab := RunISAlphaBreakdown(windows, plan, defaultDCA())

	if len(ab.Windows) != 0 {
		t.Errorf("all-fatal should yield empty Windows, got %+v", ab.Windows)
	}
	if ab.Version != resultpkg.AlphaBreakdownVersionV1 {
		t.Errorf("Version still expected, got %q", ab.Version)
	}
}

func TestRunISAlphaBreakdown_NilPlanSafe(t *testing.T) {
	ab := RunISAlphaBreakdown(nil, nil, defaultDCA())
	if len(ab.Windows) != 0 {
		t.Errorf("nil plan should yield empty Windows, got %+v", ab.Windows)
	}
}
