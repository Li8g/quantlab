// Package demo is a placeholder strategy used to unblock engine plumbing
// during the prototype phase. Step() returns no orders; the four design
// spaces (market state / macro engine / chromosome / DeadBTC release)
// from docs/策略数学引擎.md are intentionally left empty.
//
// [INVENTED v1 — replace before Phase 5+ produces meaningful Challengers.]
//
// 铁律 1: same Step() runs in backtest and live; no environment branches.
// 铁律 2: no wall-clock reads inside Step() — time comes from input.NowMs.
package demo

import "quantlab/internal/strategy"

// StrategyID is the stable identifier persisted on EvolutionTask and
// GeneRecord rows. Changing this value is a migration event.
const StrategyID = "demo"

// Step is the per-tick decision function. The placeholder implementation
// is intentionally a no-op: empty orders, RuntimeState is passed through
// from the input untouched, no DebugSnapshot.
//
// When this is replaced with a real strategy:
//   - Derive time only from input.NowMs (never from the OS clock).
//   - Read input.Closes / input.Portfolio / input.Chromosome.
//   - Emit MacroOrders / MicroOrders / ReleaseIntents.
//   - Update RuntimeState (opaque blob, strategy-private schema).
//   - Friction is applied OUTSIDE Step() — by the backtest adapter or
//     the Agent fill report — so do not read input.Chromosome's friction
//     parameters here.
func Step(input strategy.StrategyInput) strategy.StrategyOutput {
	return strategy.StrategyOutput{
		RuntimeState: input.RuntimeState,
	}
}
