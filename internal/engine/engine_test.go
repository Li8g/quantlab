package engine_test

import (
	"context"
	"math"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/engine"
	"quantlab/internal/fitness"
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
		LotMin:      0.001,
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
		Windows:     windows,
		Friction:    domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5},
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

// TestEarlyStopShortensRunOnPlateau drives the toy GA past its
// convergence point with a generous MaxGenerations cap and asserts
// the run terminates ≤ a fraction of that cap because the patience
// counter trips. Toy converges to (0.42, -0.3) within ~10 gens; with
// EarlyStopPatience=5 the engine should exit well before 100.
func TestEarlyStopShortensRunOnPlateau(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 30
	cfg.MaxGenerations = 100
	cfg.EarlyStopPatience = 5
	cfg.EarlyStopMinDelta = 0.001
	cfg.EpochSeed = 42

	eng := engine.New(toy.New(), cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	// Anything ≥ 50 means early stop didn't fire — toy converges fast
	// so 50 is a very loose ceiling.
	if result.Generations >= 50 {
		t.Errorf("Generations = %d, want < 50 (early stop should fire)",
			result.Generations)
	}
	t.Logf("toy converged in %d gens (cap 100, patience 5)", result.Generations)
}

// TestEarlyStopDisabledRunsToMaxGen confirms that EarlyStopPatience=0
// keeps the engine at its prior fixed-loop behaviour — the default
// values can't accidentally bind on callers who don't want them.
func TestEarlyStopDisabledRunsToMaxGen(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 10
	cfg.MaxGenerations = 8
	cfg.EarlyStopPatience = 0 // disabled
	cfg.EpochSeed = 1

	eng := engine.New(toy.New(), cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if result.Generations != cfg.MaxGenerations {
		t.Errorf("Generations = %d, want %d (patience disabled)",
			result.Generations, cfg.MaxGenerations)
	}
}

// TestMutationRampPreservesDeterminism — adding the ramp must not
// break the (seed, config) → BestGene contract. The ramp state is
// driven by best-score observations which are themselves
// deterministic under a fixed seed.
func TestMutationRampPreservesDeterminism(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 20
	cfg.MaxGenerations = 15
	cfg.EpochSeed = 99

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
		t.Errorf("ramp broke determinism:\n  r1=%v\n  r2=%v", r1.BestGene, r2.BestGene)
	}
	if r1.Generations != r2.Generations {
		t.Errorf("early-stop nondeterministic: %d vs %d", r1.Generations, r2.Generations)
	}
}

// TestRunEpoch_PopulatesBestRawEvaluate asserts the new field is
// non-nil with a CrucibleResult per plan window. The SaaS Epoch
// service (Phase 5D) needs this raw to build EvaluationLayer
// without keeping every gene's raw in memory through the GA loop.
func TestRunEpoch_PopulatesBestRawEvaluate(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 10
	cfg.MaxGenerations = 3
	cfg.EpochSeed = 13

	eng := engine.New(toy.New(), cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if result.BestRawEvaluate == nil {
		t.Fatal("BestRawEvaluate is nil")
	}
	if got, want := len(result.BestRawEvaluate.Windows), 4; got != want {
		t.Errorf("BestRawEvaluate.Windows len = %d, want %d (four crucible windows)", got, want)
	}
	// FrictionActual carried verbatim from plan.Friction.
	if result.BestRawEvaluate.FrictionActual.TakerFeeBPS != 10 {
		t.Errorf("FrictionActual.TakerFeeBPS = %v, want 10",
			result.BestRawEvaluate.FrictionActual.TakerFeeBPS)
	}
}

// TestRunEpoch_BestRawEvaluateMatchesBestScore is the determinism
// proof: re-evaluating the winning gene and re-aggregating must
// reproduce the BestScore that the worker pool computed during the
// generation loop. If this ever fails, either Adapter.Evaluate is no
// longer pure of (gene, plan) or fitness.AggregateScoreTotal is no
// longer deterministic under the same windows.
func TestRunEpoch_BestRawEvaluateMatchesBestScore(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 12
	cfg.MaxGenerations = 4
	cfg.EpochSeed = 5

	eng := engine.New(toy.New(), cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}

	reaggregated := fitness.AggregateScoreTotal(
		result.BestRawEvaluate.Windows,
		engine.WindowWeights(),
		cfg.LambdaCons,
		resultpkg.FitnessVersionV1RawStd,
	)
	if reaggregated.Fatal != result.BestScore.Fatal {
		t.Errorf("Fatal mismatch: re-agg=%v vs best=%v", reaggregated.Fatal, result.BestScore.Fatal)
	}
	if !result.BestScore.Fatal {
		if reaggregated.Value == nil || result.BestScore.Value == nil {
			t.Fatalf("non-Fatal with nil Value: re-agg=%v best=%v", reaggregated.Value, result.BestScore.Value)
		}
		if math.Abs(*reaggregated.Value-*result.BestScore.Value) > 1e-12 {
			t.Errorf("re-aggregated Value drift: %v vs %v",
				*reaggregated.Value, *result.BestScore.Value)
		}
	}
}

// TestRunEpoch_BuildChallengerPackageFromResult chains EpochResult
// into engine.BuildChallengerPackage and asserts the produced package
// satisfies resultpkg.Validate's cross-field equality contract. This
// is the closest assertion to "Phase 5D could pick this up and
// persist it" without an actual DB call.
func TestRunEpoch_BuildChallengerPackageFromResult(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 8
	cfg.MaxGenerations = 3
	cfg.EpochSeed = 17

	eng := engine.New(toy.New(), cfg)
	result, err := eng.RunEpoch(context.Background(), miniPlan())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}

	bc := engine.BuildContext{
		ChallengerID:      "ch-engine-test",
		Pair:              "BTCUSDT",
		DataVersion:       "binance/v1",
		EngineVersion:     "engine-test",
		StrategyVersion:   "toy/v1",
		HardwareSignature: "linux/amd64/test",
		GoVersion:         "go1.23",
		BuildID:           "test-build",
		PlanHash:          "deadbeef",
		BarsHash:          "cafef00d",
	}
	pkg, err := engine.BuildChallengerPackage(
		toy.New(),
		miniPlan(),
		result.BestGene,
		result.BestRawEvaluate,
		result.BestScore,
		cfg,
		bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage: %v", err)
	}
	if err := pkg.Validate(); err != nil {
		t.Errorf("package Validate: %v", err)
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
