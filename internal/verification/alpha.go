// In-sample alpha breakdown (backlog A3). For each crucible window, reports
// the annualized strategy return vs the monthly/weekly DCA baselines — the
// same alpha measure RunOOS produces, but per IS window instead of on the
// holdout. Diagnostic only: it feeds EvaluationLayer.AlphaBreakdown and is
// NEVER a gate (OOS already gates Promote via DecisionColor).
package verification

import (
	"math"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/resultpkg"
)

// RunISAlphaBreakdown computes per-window annualized alpha for the in-sample
// windows. It does NOT re-run the strategy: the strat return is read from each
// window's already-computed SliceScore.Value (a log return, per
// evaluate_window.go), and only the cheap DCA baseline is re-simulated on the
// window's eval bars (warmup excluded — mirroring RunOOS decision #2).
//
// The function is pure, deterministic, and returns no error: alpha is
// diagnostic, so it must never fail an epoch (decision A3). Windows that are
// Fatal, cascade-skipped, or degenerate (non-positive span, too few bars, or
// a non-finite result) carry no strat return and are simply omitted, which
// also keeps the output JSON-marshalable (no NaN/Inf).
func RunISAlphaBreakdown(
	windows []resultpkg.CrucibleResult,
	plan *domain.EvaluablePlan,
	dcaCfg fitness.GhostDCAConfig,
) resultpkg.ISAlphaBreakdown {
	out := resultpkg.ISAlphaBreakdown{Version: resultpkg.AlphaBreakdownVersionV1}
	if plan == nil {
		return out
	}

	byName := make(map[resultpkg.WindowName]domain.CrucibleWindow, len(plan.Windows))
	for _, w := range plan.Windows {
		byName[w.Name] = w
	}

	for _, cr := range windows {
		// Only normal (non-Fatal, non-skipped) windows have a strat return.
		if cr.Score.Fatal || cr.SkippedBy != nil || cr.Score.Value == nil {
			continue
		}
		pw, ok := byName[cr.Window]
		if !ok || pw.WarmupLen < 0 || pw.WarmupLen >= len(pw.Bars) {
			continue
		}
		evalBars := pw.Bars[pw.WarmupLen:]
		if len(evalBars) < 2 {
			continue
		}
		spanMs := evalBars[len(evalBars)-1].OpenTime - evalBars[0].OpenTime
		years := float64(spanMs) / (yearDays * float64(dayMs))
		if years <= 0 {
			continue
		}

		// Strat return is a log return (evaluate_window.go:241) → exp;
		// DCA ROI is a simple ROI → annualizeROI. Mirrors RunOOS exactly.
		stratAnn := math.Exp(*cr.Score.Value/years) - 1
		monthlyAnn := annualizeROI(fitness.SimulateGhostDCAMonthly(dcaCfg, evalBars, plan.Friction).ROI, years)
		weeklyAnn := annualizeROI(fitness.SimulateGhostDCAWeekly(dcaCfg, evalBars, plan.Friction).ROI, years)

		alphaMonthly := stratAnn - monthlyAnn
		alphaWeekly := stratAnn - weeklyAnn
		if !allFinite(stratAnn, monthlyAnn, weeklyAnn, alphaMonthly, alphaWeekly) {
			continue
		}

		out.Windows = append(out.Windows, resultpkg.WindowAlpha{
			Window:          cr.Window,
			StratAnn:        stratAnn,
			DCAMonthlyAnn:   monthlyAnn,
			DCAWeeklyAnn:    weeklyAnn,
			AlphaMonthlyAnn: alphaMonthly,
			AlphaWeeklyAnn:  alphaWeekly,
		})
	}
	return out
}

func allFinite(xs ...float64) bool {
	for _, x := range xs {
		if math.IsNaN(x) || math.IsInf(x, 0) {
			return false
		}
	}
	return true
}
