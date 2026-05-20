package sigmoid_v1

import (
	"testing"

	"quantlab/internal/strategy"
)

// TestSigmoid_SatisfiesRuntimeStrategy is a compile-time assertion that
// *Sigmoid implements the strategy.RuntimeStrategy interface. Failure
// here means Phase 6 Tick cannot use sigmoid_v1 — a load-bearing
// invariant we want caught at `go test` time, not in production.
func TestSigmoid_SatisfiesRuntimeStrategy(t *testing.T) {
	var _ strategy.RuntimeStrategy = (*Sigmoid)(nil)
}
