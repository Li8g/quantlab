package demo

import (
	"encoding/json"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// TestStep_NoOpReturnsEmpty verifies the placeholder Step() emits no orders.
func TestStep_NoOpReturnsEmpty(t *testing.T) {
	in := strategy.StrategyInput{
		NowMs:                1_700_000_000_000,
		Closes:               []float64{49500, 50000, 50500},
		Timestamps:           []int64{1, 2, 3},
		Portfolio:            strategy.PortfolioSnapshot{USDT: 10_000},
		Chromosome:           domain.Gene{0.5, 0.25, 0.1},
		Spawn:                resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		LastProcessedBarTime: 0,
	}
	out := Step(in)
	if len(out.MacroOrders) != 0 || len(out.MicroOrders) != 0 || len(out.ReleaseIntents) != 0 {
		t.Errorf("placeholder Step should produce no orders, got %+v", out)
	}
	if out.DebugSnapshot != nil {
		t.Errorf("placeholder Step should not produce DebugSnapshot")
	}
}

// TestStep_Deterministic verifies that running Step() twice on the same
// input produces the same output (no hidden state, no wall-clock reads).
func TestStep_Deterministic(t *testing.T) {
	state := json.RawMessage(`{"last_signal":0.1}`)
	in := strategy.StrategyInput{
		NowMs:        1_700_000_000_000,
		Closes:       []float64{1, 2, 3},
		Timestamps:   []int64{1, 2, 3},
		Chromosome:   domain.Gene{0.1, 0.2},
		RuntimeState: state,
	}
	a := Step(in)
	b := Step(in)
	if string(a.RuntimeState) != string(b.RuntimeState) {
		t.Errorf("non-deterministic RuntimeState:\n  a=%s\n  b=%s", a.RuntimeState, b.RuntimeState)
	}
	if len(a.MacroOrders) != len(b.MacroOrders) || len(a.MicroOrders) != len(b.MicroOrders) {
		t.Errorf("non-deterministic order counts")
	}
}

// TestStep_PassesRuntimeStateThrough verifies the placeholder echoes the
// incoming RuntimeState bytes unchanged.
func TestStep_PassesRuntimeStateThrough(t *testing.T) {
	state := json.RawMessage(`{"foo":"bar"}`)
	in := strategy.StrategyInput{
		NowMs:        1_700_000_000_000,
		RuntimeState: state,
	}
	out := Step(in)
	if string(out.RuntimeState) != string(state) {
		t.Errorf("RuntimeState not echoed:\n  in =%s\n  out=%s", string(state), string(out.RuntimeState))
	}
}
