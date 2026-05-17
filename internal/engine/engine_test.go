package engine_test

import (
	"context"
	"math"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/engine"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategies/toy"
)

// miniPlan builds an EvaluablePlan with all four windows (one bar each).
// Bars content is irrelevant — the toy strategy is plan-independent.
func miniPlan() *domain.EvaluablePlan {
	windows := make([]domain.CrucibleWindow, 0, 4)
	for _, name := range resultpkg.AllWindowsInEvalOrder() {
		windows = append(windows, domain.CrucibleWindow{
			Name: name,
			Bars: []domain.Bar{{OpenTime: 1, Close: 100}},
		})
	}
	return &domain.EvaluablePlan{
		Pair:     "BTCUSDT",
		Spawn:    resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		LotStep:  0.0001,
		LotMin:   0.001,
		Windows:  windows,
		Friction: domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5},
	}
}

// TestGAConvergesOnToy is the headline Phase 5B validation. Targets are
// (0.42, -0.3); with 50 individuals × 30 generations the simple GA must
// land within 0.05 of each target. Tolerant on purpose — this test is a
// regression guard against engine bugs, not a benchmark of GA quality.
func TestGAConvergesOnToy(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 50
	cfg.MaxGenerations = 30
	cfg.EpochSeed = 42

	eng := engine.New(toy.New(), cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch err: %v", err)
	}
	if result.BestScore.Fatal {
		t.Fatal("best individual is Fatal — engine broke")
	}

	const (
		targetAlpha = 0.42
		targetBeta  = -0.3
		tol         = 0.05
	)
	dAlpha := math.Abs(result.BestGene[0] - targetAlpha)
	dBeta := math.Abs(result.BestGene[1] - targetBeta)
	if dAlpha > tol || dBeta > tol {
		t.Errorf("did not converge: best=%v (Δα=%g, Δβ=%g, tol=%g, score=%g)",
			result.BestGene, dAlpha, dBeta, tol, *result.BestScore.Value)
	}
	t.Logf("converged: gene=%v score=%g fp=%s", result.BestGene, *result.BestScore.Value, result.BestFingerprint)
}

// TestRunEpochDeterministic: same EpochSeed → identical best gene across
// runs, even though evaluatePopulation uses goroutines (worker pickup
// order is nondeterministic but Adapter.Evaluate is pure of (gene, plan),
// so per-index scores are stable).
func TestRunEpochDeterministic(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 30
	cfg.MaxGenerations = 5
	cfg.EpochSeed = 7

	eng := engine.New(toy.New(), cfg)
	r1, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatal(err)
	}
	r2, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatal(err)
	}
	if !geneEqual(r1.BestGene, r2.BestGene) {
		t.Errorf("nondeterministic: %v vs %v", r1.BestGene, r2.BestGene)
	}
	if r1.BestFingerprint != r2.BestFingerprint {
		t.Errorf("fingerprint differs: %s vs %s", r1.BestFingerprint, r2.BestFingerprint)
	}
}

func TestRunEpoch_RejectsBadConfig(t *testing.T) {
	cases := []struct {
		name string
		mut  func(*engine.EngineConfig)
	}{
		{"PopSize<2", func(c *engine.EngineConfig) { c.PopSize = 1 }},
		{"MaxGenerations<1", func(c *engine.EngineConfig) { c.MaxGenerations = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := engine.DefaultConfig()
			c.mut(&cfg)
			eng := engine.New(toy.New(), cfg)
			if _, err := eng.RunEpoch(context.Background(), miniPlan()); err == nil {
				t.Error("expected error for bad config")
			}
		})
	}
}

func TestRunEpoch_RejectsNilPlan(t *testing.T) {
	eng := engine.New(toy.New(), engine.DefaultConfig())
	if _, err := eng.RunEpoch(context.Background(), nil); err == nil {
		t.Error("expected error for nil plan")
	}
}

func geneEqual(a, b domain.Gene) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
