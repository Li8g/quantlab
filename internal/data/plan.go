// BuildEvaluablePlan composes BuildCrucibleWindows + Ghost DCA baselines
// + canonical-JSON hashes into a single EvaluablePlan suitable for
// internal/engine.RunEpoch. Source-of-truth:
// docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 5C-plan +
// internal/quant/canonical_json.go frozen comment.
//
// The function is a thin assembler: BuildCrucibleWindows for IS+OOS
// slicing, fitness.SimulateGhostDCA{Monthly,Weekly} for baseline
// references (run over IS bars only so they mirror what the GA sees),
// quant.BarsHash + quant.PlanHash for reproducibility metadata.
//
// Returned PlanHash and BarsHash flow into BuildContext (engine/
// package.go) — they replace the deadbeef/cafef00d placeholders that
// Phase 5B-repo unit tests still use.
package data

import (
	"fmt"

	"quantlab/internal/domain"
	"quantlab/internal/fitness"
	"quantlab/internal/quant"
	"quantlab/internal/resultpkg"
)

// PlanOptions bundles every knob BuildEvaluablePlan needs. Grouped
// into one struct so callers can populate it field-by-field without
// fighting a 9-arg signature.
//
// Friction holds EFFECTIVE values — TestMode coercion (taker_fee=0,
// slippage=0 when test_mode=true) is the caller's responsibility; this
// builder embeds whatever it receives verbatim into the plan and into
// the GhostDCA simulator.
type PlanOptions struct {
	Pair       string
	Spawn      resultpkg.SpawnPointPayload
	WarmupDays int
	OosDays    *int
	Friction   domain.FrictionParams
	LotStep    float64
	LotMin     float64
	DCA        fitness.GhostDCAConfig
}

// BuildEvaluablePlan returns the assembled plan plus its plan_hash and
// bars_hash. PlanHash is computed AFTER all in-plan fields are populated,
// so callers must not mutate the plan after the call.
//
// Returned errors wrap the underlying BuildCrucibleWindows / hash error
// verbatim with a "data.BuildEvaluablePlan:" prefix; callers should
// log the chain rather than re-inspect.
func BuildEvaluablePlan(bars []domain.Bar, opts PlanOptions) (*domain.EvaluablePlan, string, string, error) {
	is, oos, err := BuildCrucibleWindows(bars, opts.WarmupDays, opts.OosDays)
	if err != nil {
		return nil, "", "", fmt.Errorf("data.BuildEvaluablePlan: %w", err)
	}

	// DCA baselines run on the IS bars only. OOS is for post-GA
	// fairness verification — it must not influence in-generation
	// scoring, so the baselines it produces would mislead alpha
	// comparisons. Slice via the first OOS bar's OpenTime so a
	// pre-validated, strictly-ascending series stays consistent.
	isBars := bars
	if oos != nil && len(oos.Bars) > 0 {
		oosStartTS := oos.Bars[0].OpenTime
		for i, b := range bars {
			if b.OpenTime >= oosStartTS {
				isBars = bars[:i]
				break
			}
		}
	}

	monthly := fitness.SimulateGhostDCAMonthly(opts.DCA, isBars, opts.Friction)
	weekly := fitness.SimulateGhostDCAWeekly(opts.DCA, isBars, opts.Friction)

	plan := &domain.EvaluablePlan{
		Pair:    opts.Pair,
		Spawn:   opts.Spawn,
		LotStep: opts.LotStep,
		LotMin:  opts.LotMin,
		Windows: is,
		DCABaselines: domain.DCABaselines{
			Monthly: domain.DCABaseline{
				FinalEquity:   monthly.FinalEquity,
				TotalInjected: monthly.TotalInjected,
				MaxDrawdown:   monthly.MaxDrawdown,
			},
			Weekly: domain.DCABaseline{
				FinalEquity:   weekly.FinalEquity,
				TotalInjected: weekly.TotalInjected,
				MaxDrawdown:   weekly.MaxDrawdown,
			},
		},
		OosWindow: oos,
		Friction:  opts.Friction,
	}

	barsHash, err := quant.BarsHash(bars)
	if err != nil {
		return nil, "", "", fmt.Errorf("data.BuildEvaluablePlan: %w", err)
	}
	planHash, err := quant.PlanHash(plan)
	if err != nil {
		return nil, "", "", fmt.Errorf("data.BuildEvaluablePlan: %w", err)
	}

	return plan, planHash, barsHash, nil
}
