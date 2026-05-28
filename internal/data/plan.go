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
	Pair        string
	Spawn       resultpkg.SpawnPointPayload
	WarmupDays  int
	OosDays     *int
	Friction    domain.FrictionParams
	LotStep     float64
	LotMin      float64
	FatalMDD    float64
	InitialUSDT float64
	DCA         fitness.GhostDCAConfig

	// MinEvalBars is the strategy's MinEvalBars() value. BuildEvaluablePlan
	// rejects plans whose IS windows fit in calendar days but starve the
	// strategy's internal lookback — defense in depth ahead of
	// engine.RunEpoch's identical check (engine.go ~line 192). Zero means
	// "no check" so engine-only tests that don't model a strategy can
	// still call BuildEvaluablePlan; production callers always set it
	// from strat.MinEvalBars().
	MinEvalBars int
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
	if len(is) == 0 {
		spanDays := float64(bars[len(bars)-1].OpenTime-bars[0].OpenTime) / float64(DayMs)
		return nil, "", "", fmt.Errorf(
			"data.BuildEvaluablePlan: no crucible window fits (bar span %.1fd < warmup %dd + smallest eval 183d/6m); load more bars or lower warmup_days",
			spanDays, opts.WarmupDays,
		)
	}

	// MinEvalBars guard. Engine performs the same check in RunEpoch; we
	// duplicate it here so the synchronous task-creation path surfaces
	// the failure at plan build time, with a message that names the
	// strategy's required bar count (operators can act without reading
	// the engine source). Skipped when MinEvalBars is unset so tests
	// that don't supply a strategy stay green.
	if opts.MinEvalBars > 0 {
		for _, w := range is {
			if len(w.Bars) < opts.MinEvalBars {
				return nil, "", "", fmt.Errorf(
					"data.BuildEvaluablePlan: window %q has %d bars, below MinEvalBars=%d; load more bars, choose a coarser interval, or drop window from cascade",
					w.Name, len(w.Bars), opts.MinEvalBars,
				)
			}
		}
	}

	// DCA baselines run on the IS bars only. OOS is for post-GA
	// fairness verification — it must not influence in-generation
	// scoring, so the baselines it produces would mislead alpha
	// comparisons. Slice via OosWindow.StartTS (the eval-start TS,
	// post-warmup). OosWindow.Bars[0] is the warmup-start now that
	// crucible.BuildCrucibleWindows attaches a warmup prefix, so
	// using oos.Bars[0].OpenTime would over-truncate IS into the
	// warmup region — which is still IS time, just borrowed for
	// strategy indicator convergence on OOS.
	isBars := bars
	if oos != nil {
		for i, b := range bars {
			if b.OpenTime >= oos.StartTS {
				isBars = bars[:i]
				break
			}
		}
	}

	monthly := fitness.SimulateGhostDCAMonthly(opts.DCA, isBars, opts.Friction)
	weekly := fitness.SimulateGhostDCAWeekly(opts.DCA, isBars, opts.Friction)

	plan := &domain.EvaluablePlan{
		Pair:        opts.Pair,
		Spawn:       opts.Spawn,
		LotStep:     opts.LotStep,
		LotMin:      opts.LotMin,
		FatalMDD:    opts.FatalMDD,
		InitialUSDT: opts.InitialUSDT,
		Windows:     is,
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
