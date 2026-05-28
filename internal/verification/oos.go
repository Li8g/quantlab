// OOS Anchored Holdout verification (Phase 5D).
//
// RunOOS replays the elected champion gene on the OOS bars carved out
// by data.BuildCrucibleWindows, recomputes a DCA baseline on those
// same OOS bars (decision #2: OOS 段重算, NOT reuse IS DCA baselines),
// and reports an annualized excess return as the alpha measure
// (decision #1: 年化超额收益率, not period-total ratio).
//
// IS scoring is unaffected (decision #3: OOS Fatal does not influence
// IS ScoreTotal). This function runs AFTER engine.RunEpoch returns;
// the engine never sees OOS outcomes.
//
// Status semantics:
//
//	ok                — both alphas computed, DecisionColor set per §thresholds.
//	insufficient_data — OosWindow nil/empty, or span < MinOOSDays (90).
//	                    (decision #4: 90-day floor; task is NOT rejected,
//	                    OOSResult is gray.)
//	failed            — strategy Fatal on OOS bars OR NAV degenerate.
//	                    DecisionColor=red, Notes carries Fatal reason.
//
// DecisionColor thresholds (annualized; see §1 comment block below):
//
//	green:  alpha_monthly_ann ≥ +5%  AND  alpha_weekly_ann ≥ 0
//	yellow: -3% < alpha_monthly_ann < +5%   (the default pool)
//	red:    alpha_monthly_ann ≤ -3%   OR Fatal
//	gray:   Status != ok
//
// Asymmetric thresholds reflect cost asymmetry: a false green (deploy
// a bad strategy) is far more expensive than a false red (re-run after
// reviewer notices). Symmetric ±X would attention-tax reviewers on
// noise. See conversation 2026-05-28 for the noise-band analysis.
//
// Known limitation (not in 5D scope, flagged in Notes when status=ok):
// plan.OosWindow.WarmupLen=0 — the strategy's indicators (EMAs etc.)
// start cold on OOS bars and the score is artificially depressed
// compared to IS windows that get a 365-day warmup. The 90-day floor
// gives partial implicit warmup but not enough for the longest
// indicator (sigmoid_v1's max EMA ≈ 300 bars). Follow-up: extend
// data.BuildCrucibleWindows to attach a warmup prefix to OosWindow
// and bump WarmupLen accordingly.
package verification

import (
	"context"
	"encoding/json"
	"fmt"
	"math"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// MinOOSDays is the lower span bound below which RunOOS returns
// insufficient_data instead of attempting an evaluation. Per decision
// #4 (2026-05-28): 90 days. Below this the alpha estimate is dominated
// by noise (see §3 noise-band analysis in the same discussion).
const MinOOSDays = 90

// DecisionColor thresholds — annualized excess return (strategy_ann -
// dca_monthly_ann). See package doc for rationale.
const (
	OOSGreenAlphaMonthlyAnn = 0.05  // ≥ +5%/year monthly alpha — green
	OOSRedAlphaMonthlyAnn   = -0.03 // ≤ -3%/year monthly alpha — red
)

const (
	dayMs    = int64(24 * 60 * 60 * 1000)
	yearDays = 365.25
)

// RunOOS evaluates bestGene on plan.OosWindow and assembles an
// OOSResult. The caller (epoch.Service) is expected to marshal the
// returned struct into BuildContext.OOSPayload before
// BuildChallengerPackage.
//
// dcaCfg is the same GhostDCAConfig the GA used for IS scoring; the
// OOS DCA baseline is simulated on the OOS bars with this config so
// the comparison is apples-to-apples.
//
// Determinism: the result is deterministic given (strat, plan,
// bestGene, dcaCfg). The function constructs a fresh Adapter via
// strat.NewAdapter(oosPlan) and calls Reset before Evaluate to honour
// the §5.6 isolation contract.
func RunOOS(
	ctx context.Context,
	strat strategy.EvolvableStrategy,
	plan *domain.EvaluablePlan,
	bestGene domain.Gene,
	dcaCfg fitness.GhostDCAConfig,
) (*resultpkg.OOSResult, error) {
	res := &resultpkg.OOSResult{}

	if plan == nil || plan.OosWindow == nil || len(plan.OosWindow.Bars) == 0 {
		notes := "no oos window in plan (oos_days not requested or 0 bars)"
		res.Status = resultpkg.VerificationStatusInsufficientData
		res.Notes = &notes
		return res, nil
	}

	oosBars := plan.OosWindow.Bars
	spanMs := oosBars[len(oosBars)-1].OpenTime - oosBars[0].OpenTime
	spanDays := float64(spanMs) / float64(dayMs)
	if spanDays < float64(MinOOSDays) {
		notes := fmt.Sprintf("oos span %.1f days < %d minimum", spanDays, MinOOSDays)
		res.Status = resultpkg.VerificationStatusInsufficientData
		res.Notes = &notes
		return res, nil
	}

	// OOS-only plan. WindowOOS is excluded from
	// resultpkg.AllWindowsInEvalOrder, so the strategy's cascade
	// iterator would skip it. Relabel as Window6M — the cascade body
	// is window-name-blind for arithmetic, the label is only used as
	// the map key for the iteration order. OosWindow=nil prevents any
	// downstream recursion if a strategy reads it.
	oosAsWindow := *plan.OosWindow
	oosAsWindow.Name = resultpkg.Window6M
	oosPlan := &domain.EvaluablePlan{
		Pair:         plan.Pair,
		Spawn:        plan.Spawn,
		LotStep:      plan.LotStep,
		LotMin:       plan.LotMin,
		FatalMDD:     plan.FatalMDD,
		InitialUSDT:  plan.InitialUSDT,
		Windows:      []domain.CrucibleWindow{oosAsWindow},
		DCABaselines: plan.DCABaselines, // unused by sigmoid_v1 evaluate
		OosWindow:    nil,
		Friction:     plan.Friction,
	}

	adapter, err := strat.NewAdapter(oosPlan)
	if err != nil {
		return nil, fmt.Errorf("verification.RunOOS: NewAdapter: %w", err)
	}
	defer adapter.Close()
	if err := adapter.Reset(oosPlan); err != nil {
		return nil, fmt.Errorf("verification.RunOOS: Reset: %w", err)
	}
	raw, err := adapter.Evaluate(bestGene)
	if err != nil {
		return nil, fmt.Errorf("verification.RunOOS: Evaluate: %w", err)
	}
	if len(raw.Windows) != 1 {
		return nil, fmt.Errorf("verification.RunOOS: expected 1 OOS window result, got %d", len(raw.Windows))
	}
	_ = ctx // accepted for API symmetry / future cancellation; strategy.Evaluate doesn't honour ctx today

	sw := raw.Windows[0]

	// Fatal on OOS → status=failed + red, IS score untouched (decision #3).
	if sw.Score.Fatal {
		reason := "strategy Fatal on OOS"
		if sw.FatalReason != nil {
			reason = fmt.Sprintf("strategy Fatal on OOS: %s", *sw.FatalReason)
		}
		color := resultpkg.DecisionColorRed
		res.Status = resultpkg.VerificationStatusFailed
		res.DecisionColor = &color
		res.Notes = &reason
		return res, nil
	}
	if sw.Score.Value == nil {
		// CrucibleResult invariant: non-Fatal + non-SkippedBy ⇒ Value!=nil.
		// If we ever see this it's a strategy bug.
		return nil, fmt.Errorf("verification.RunOOS: non-Fatal OOS window with nil Value")
	}

	years := float64(spanMs) / (yearDays * float64(dayMs))
	if years <= 0 {
		return nil, fmt.Errorf("verification.RunOOS: non-positive years %g (span %dms)", years, spanMs)
	}

	// Strategy return: sigmoid_v1's SliceScore.Value is the log return
	// log(navFinal / navAtWarmupStart) — see evaluate_window.go:241.
	// Annualized return = exp(log_total / years) - 1.
	stratLog := *sw.Score.Value
	stratAnn := math.Exp(stratLog/years) - 1

	// DCA recompute on OOS bars (decision #2). Use the same GhostDCAConfig
	// and Friction the GA used so the comparison is apples-to-apples.
	dcaMonthly := fitness.SimulateGhostDCAMonthly(dcaCfg, oosBars, plan.Friction)
	dcaWeekly := fitness.SimulateGhostDCAWeekly(dcaCfg, oosBars, plan.Friction)
	monthlyAnn := annualizeROI(dcaMonthly.ROI, years)
	weeklyAnn := annualizeROI(dcaWeekly.ROI, years)

	alphaMonthly := stratAnn - monthlyAnn
	alphaWeekly := stratAnn - weeklyAnn
	color := classifyOOSColor(alphaMonthly, alphaWeekly)

	notes := fmt.Sprintf(
		"oos span %.1fd, strat_ann=%.4f, dca_monthly_ann=%.4f, dca_weekly_ann=%.4f; warmup_len=0 (indicators cold-started — see oos.go header)",
		spanDays, stratAnn, monthlyAnn, weeklyAnn,
	)
	res.Status = resultpkg.VerificationStatusOK
	res.OOSAlphaMonthly = &alphaMonthly
	res.OOSAlphaWeekly = &alphaWeekly
	res.DecisionColor = &color
	res.Notes = &notes
	return res, nil
}

// annualizeROI converts a total-period ROI (e.g. 0.42 = +42% over the
// whole OOS span) into an annualized rate via (1+roi)^(1/years)-1.
// Guards against 1+roi ≤ 0 (degenerate baseline) by clamping to -1.
func annualizeROI(roi, years float64) float64 {
	base := 1 + roi
	if base <= 0 {
		return -1
	}
	return math.Pow(base, 1/years) - 1
}

// classifyOOSColor maps (alpha_monthly_ann, alpha_weekly_ann) to a
// DecisionColor per the asymmetric thresholds in the package doc.
//
// Logic order matters: green requires both monthly AND weekly to hold,
// so it's checked first; red is the absolute-floor check on monthly;
// yellow is everything in between.
func classifyOOSColor(alphaMonth, alphaWeek float64) resultpkg.DecisionColor {
	switch {
	case alphaMonth >= OOSGreenAlphaMonthlyAnn && alphaWeek >= 0:
		return resultpkg.DecisionColorGreen
	case alphaMonth <= OOSRedAlphaMonthlyAnn:
		return resultpkg.DecisionColorRed
	default:
		return resultpkg.DecisionColorYellow
	}
}

// MarshalOOSPayload is a convenience for callers that need to stuff
// the result into engine.BuildContext.OOSPayload (which is
// json.RawMessage to avoid coupling engine to verification types).
// Returns nil + nil error when res is nil.
func MarshalOOSPayload(res *resultpkg.OOSResult) (json.RawMessage, error) {
	if res == nil {
		return nil, nil
	}
	return json.Marshal(res)
}
