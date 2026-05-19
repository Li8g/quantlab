// Strategy registry: a small map[StrategyID]Factory that the HTTP →
// Epoch service path consults before constructing an EvolvableStrategy.
// Source-of-truth: CLAUDE.md "two-layer hard boundary" — the engine
// never imports strategy internals; the registry is the only thing in
// internal/saas that does.
//
// Registry contents are explicit per Phase 5D decision: factory funcs
// registered in code, not via init() side-effects. DefaultRegistry
// bootstraps sigmoid_v1; cmd/saas calls it during startup.
package epoch

import (
	"fmt"

	"quantlab/internal/strategies/sigmoid_v1"
	"quantlab/internal/strategy"
)

// Factory builds an EvolvableStrategy from the bar-interval that the
// CreateEvolutionTaskRequest specified. The interval is required
// because some strategies (sigmoid_v1) are bound to a single interval
// at construction (sigmoid_v1.New(barIntervalMs)).
type Factory func(barIntervalMs int64) strategy.EvolvableStrategy

// Registry maps StrategyID → Factory. Safe for read-after-init
// concurrent use; mutations (Register) must happen before serving HTTP.
type Registry struct {
	entries map[string]Factory
}

// NewRegistry returns an empty Registry.
func NewRegistry() *Registry {
	return &Registry{entries: map[string]Factory{}}
}

// Register adds (or overwrites) a factory under id. Panics on empty id
// or nil factory — both are programming errors not worth a returned err.
func (r *Registry) Register(id string, f Factory) {
	if id == "" {
		panic("epoch.Registry.Register: empty id")
	}
	if f == nil {
		panic("epoch.Registry.Register: nil factory")
	}
	r.entries[id] = f
}

// Get returns the factory for id and a boolean indicating presence.
func (r *Registry) Get(id string) (Factory, bool) {
	f, ok := r.entries[id]
	return f, ok
}

// IDs returns the set of registered strategy IDs (for logging / debug).
func (r *Registry) IDs() []string {
	out := make([]string, 0, len(r.entries))
	for id := range r.entries {
		out = append(out, id)
	}
	return out
}

// Build resolves id to a factory and constructs the strategy. Wraps
// the not-found case so callers get a single error chain.
func (r *Registry) Build(id string, barIntervalMs int64) (strategy.EvolvableStrategy, error) {
	f, ok := r.Get(id)
	if !ok {
		return nil, fmt.Errorf("epoch.Registry: unknown strategy_id %q", id)
	}
	return f(barIntervalMs), nil
}

// DefaultRegistry bootstraps the prototype-phase strategy set. cmd/saas
// calls this once at startup; tests build their own registry via
// NewRegistry + Register so the production set doesn't leak in.
func DefaultRegistry() *Registry {
	r := NewRegistry()
	r.Register(sigmoid_v1.StrategyID, func(barIntervalMs int64) strategy.EvolvableStrategy {
		return sigmoid_v1.New(barIntervalMs)
	})
	return r
}
