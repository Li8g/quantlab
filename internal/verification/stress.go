package verification

import (
	"context"
	"fmt"

	"quantlab/internal/domain"
	"quantlab/internal/strategy"
)

// DefaultStressIters is the [INVENTED v1] Monte Carlo iteration count for
// the post-epoch SBB stress test — §I-4.3 specifies the bootstrap but not
// nIter. 1000 is cheap as a one-shot and gives stable 5/50/95 quantiles.
const DefaultStressIters = 1000

// RunStress re-runs the champion gene ONCE on a CaptureReturns copy of the
// in-sample plan to recover the longest non-Fatal window's per-bar return
// series (discarded during the GA hot loop), then summarises tail risk via
// the stationary block bootstrap (OptimalBlockLength + RunMonteCarlo).
//
// Returns (nil, nil) when no usable series exists — the best gene was
// all-Fatal, or the strategy captured nothing. That is NOT an error:
// stress is diagnostic-only and the caller leaves stress_summary unset. A
// non-nil error means an Adapter/strategy contract violation (NewAdapter/
// Reset/Evaluate), same convention as RunOOS — but because stress is
// diagnostic the caller treats even that as non-fatal for the epoch.
//
// Runs AFTER RunEpoch. CaptureReturns is json:"-", so the copied plan's
// hash is unchanged and this re-run is out-of-band — it can never touch
// the IS ScoreTotal already written. seed makes the MCReport reproducible
// (the SaaS service passes the epoch seed).
func RunStress(
	ctx context.Context,
	strat strategy.EvolvableStrategy,
	plan *domain.EvaluablePlan,
	bestGene domain.Gene,
	seed uint64,
) (*MCReport, error) {
	if plan == nil {
		return nil, nil
	}
	capPlan := *plan // shallow copy; only CaptureReturns differs from the IS plan
	capPlan.CaptureReturns = true

	adapter, err := strat.NewAdapter(&capPlan)
	if err != nil {
		return nil, fmt.Errorf("verification.RunStress: NewAdapter: %w", err)
	}
	defer adapter.Close()
	if err := adapter.Reset(&capPlan); err != nil {
		return nil, fmt.Errorf("verification.RunStress: Reset: %w", err)
	}
	raw, err := adapter.Evaluate(bestGene)
	if err != nil {
		return nil, fmt.Errorf("verification.RunStress: Evaluate: %w", err)
	}
	if raw == nil || len(raw.LongestWindowReturns) == 0 {
		return nil, nil
	}
	if err := raw.ValidateForStress(); err != nil {
		return nil, fmt.Errorf("verification.RunStress: invalid raw: %w", err)
	}

	blockLen := OptimalBlockLength(raw.LongestWindowReturns)
	rep := RunMonteCarlo(raw.LongestWindowReturns, blockLen, DefaultStressIters, seed, plan.FatalMDD)
	return &rep, nil
}
