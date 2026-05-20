// RuntimeStrategy — the live-trading interface companion to
// EvolvableStrategy. Phase 6 Tick function calls Step(input) on every
// cron tick; this is the only verb live trading needs from the
// strategy.
//
// 铁律 1 (Step() isomorphism): the same Step() runs in backtest and
// live; no environment branches. The backtest path in
// internal/strategies/sigmoid_v1/evaluate_window.go calls the same
// Step that the SaaS Tick function calls — different inputs (replayed
// historical bars vs live K-line tail) feeding identical logic.
//
// 铁律 2 (no wall clock in Step()): Step must derive "current time"
// only from StrategyInput.NowMs. The Tick function reads time.Now()
// exactly once on the cron outer loop and threads it into NowMs.
package strategy

// RuntimeStrategy is the live-trading contract — a single Step verb.
// Every concrete strategy that runs in saas (cron Tick) implements
// this. Most strategies also implement EvolvableStrategy for the GA
// pipeline; the two interfaces are deliberately decoupled so that
// "evolvable but not yet live" or "live but not evolved" strategies
// remain expressible without inheritance gymnastics.
type RuntimeStrategy interface {
	// Step is the per-tick decision function. Inputs are immutable;
	// the returned StrategyOutput carries macro/micro orders, release
	// intents, the updated RuntimeState blob, and an optional
	// DebugSnapshot. See StrategyInput / StrategyOutput docs in
	// contract.go for field semantics.
	Step(input StrategyInput) (StrategyOutput, error)
}
