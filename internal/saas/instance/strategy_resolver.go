// strategy_resolver.go — default StrategyResolver backed by
// epoch.Registry. Returns both EvolvableStrategy (for DecodeElite)
// and RuntimeStrategy (for Step). Strategies that satisfy
// EvolvableStrategy but not RuntimeStrategy fail at Resolve time
// with a clear message — better fail-fast than NPE inside Tick.
package instance

import (
	"fmt"

	"quantlab/internal/saas/epoch"
	"quantlab/internal/strategy"
)

// barIntervalMs is hardcoded at 1m per docs/系统总体拓扑结构.md §6.2
// (cron Tick always feeds the strategy 1m K-lines). Multi-interval
// instances are out of Phase 6 scope; revisit in Phase 9+ if needed.
const barIntervalMs = int64(60_000)

// DefaultStrategyResolver wraps an epoch.Registry. It builds a fresh
// strategy per Resolve call; no caching — strategies are cheap to
// instantiate (constructor work only sets bar-interval and constants).
type DefaultStrategyResolver struct {
	Registry *epoch.Registry
}

func (r *DefaultStrategyResolver) Resolve(strategyID string) (strategy.EvolvableStrategy, strategy.RuntimeStrategy, error) {
	estrat, err := r.Registry.Build(strategyID, barIntervalMs)
	if err != nil {
		return nil, nil, fmt.Errorf("resolver: build %q: %w", strategyID, err)
	}
	rstrat, ok := estrat.(strategy.RuntimeStrategy)
	if !ok {
		return nil, nil, fmt.Errorf("resolver: strategy %q does not satisfy RuntimeStrategy (missing Step)", strategyID)
	}
	return estrat, rstrat, nil
}
