package epoch

import (
	"context"
	"math/rand"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategies/sigmoid_v1"
	"quantlab/internal/strategy"
)

// stubStrategy is the smallest EvolvableStrategy that satisfies the
// interface for registry tests — registry never calls verb methods,
// just stores the value and reads StrategyID back.
type stubStrategy struct{ id string }

func (s *stubStrategy) StrategyID() string                                           { return s.id }
func (s *stubStrategy) Segments() []domain.SegmentInfo                               { return nil }
func (s *stubStrategy) Sample(*rand.Rand) domain.Gene                                { return nil }
func (s *stubStrategy) Clamp(g domain.Gene) domain.Gene                              { return g }
func (s *stubStrategy) Validate(domain.Gene) error                                   { return nil }
func (s *stubStrategy) Fingerprint(domain.Gene) string                               { return "stub" }
func (s *stubStrategy) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene        { return p1 }
func (s *stubStrategy) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene { return g }
func (s *stubStrategy) MinEvalBars() int                                             { return 1 }
func (s *stubStrategy) NewAdapter(*domain.EvaluablePlan) (strategy.Adapter, error) {
	return nil, nil
}
func (s *stubStrategy) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	return nil, nil
}
func (s *stubStrategy) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (s *stubStrategy) DecodeElite(resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return nil, nil
}
func (s *stubStrategy) EncodeResult(
	domain.Gene, resultpkg.SpawnPointPayload, resultpkg.ReproducibilityMetadata,
	resultpkg.GAConfigSnapshot, *resultpkg.EvaluationLayer,
	*resultpkg.VerificationLayer, *resultpkg.DiagnosticsLayer,
) (resultpkg.ChallengerResultPackage, error) {
	return resultpkg.ChallengerResultPackage{}, nil
}

// Compile-time conformance check.
var _ strategy.EvolvableStrategy = (*stubStrategy)(nil)

func TestRegistry_GetReturnsRegisteredFactory(t *testing.T) {
	r := NewRegistry()
	r.Register("alpha", func(barIntervalMs int64) strategy.EvolvableStrategy {
		return &stubStrategy{id: "alpha"}
	})
	f, ok := r.Get("alpha")
	if !ok {
		t.Fatal("Get(alpha): not found")
	}
	got := f(60_000)
	if got.StrategyID() != "alpha" {
		t.Errorf("factory returned %q, want alpha", got.StrategyID())
	}
}

func TestRegistry_GetMissingReturnsFalse(t *testing.T) {
	r := NewRegistry()
	if _, ok := r.Get("missing"); ok {
		t.Error("Get on empty registry should return ok=false")
	}
}

func TestRegistry_BuildWrapsUnknownAsError(t *testing.T) {
	r := NewRegistry()
	_, err := r.Build("missing", 60_000)
	if err == nil {
		t.Error("Build on unknown strategy should return error")
	}
}

func TestRegistry_RegisterPanicsOnEmptyID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with empty id should panic")
		}
	}()
	NewRegistry().Register("", func(int64) strategy.EvolvableStrategy { return nil })
}

func TestRegistry_RegisterPanicsOnNilFactory(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("Register with nil factory should panic")
		}
	}()
	NewRegistry().Register("x", nil)
}

func TestDefaultRegistry_ContainsSigmoidV1(t *testing.T) {
	r := DefaultRegistry()
	if _, ok := r.Get(sigmoid_v1.StrategyID); !ok {
		t.Errorf("DefaultRegistry missing %q; got IDs=%v", sigmoid_v1.StrategyID, r.IDs())
	}
	// Build must produce a sigmoid_v1 instance bound to the supplied
	// barIntervalMs (sigmoid_v1.New panics on a non-positive value, so
	// supplying 60_000 also proves the factory plumbs it through).
	s, err := r.Build(sigmoid_v1.StrategyID, 60_000)
	if err != nil {
		t.Fatalf("Build sigmoid_v1: %v", err)
	}
	if s.StrategyID() != sigmoid_v1.StrategyID {
		t.Errorf("built strategy StrategyID = %q, want %q", s.StrategyID(), sigmoid_v1.StrategyID)
	}
}
