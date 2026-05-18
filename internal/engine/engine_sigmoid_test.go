package engine_test

import (
	"context"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/engine"
	"quantlab/internal/resultpkg"
	sigmoid "quantlab/internal/strategies/sigmoid_v1"
)

// sigmoidPlanBullish builds a 4-window EvaluablePlan whose bars
// describe a 100-day ramp from $100 → $200 followed by 100 days
// flat at $200. The market direction is unambiguous: any strategy
// that puts cash into BTC during the ramp wins; pure cash holders
// return 0. GA should discover the basic "buy on price-below-EMA"
// pattern within a few generations.
//
// All four windows use the same bar series — the engine still
// runs the canonical cascade and weight-sums into ScoreTotal, but
// the per-window scores are identical (modulo tiny stochastic
// variations from the strategy's internal logic).
func sigmoidPlanBullish() *domain.EvaluablePlan {
	const (
		barInterval = int64(24) * 60 * 60 * 1000 // 1d
		warmup      = 50
		rampLen     = 100
		flatLen     = 100
	)
	totalBars := rampLen + flatLen
	bars := make([]domain.Bar, totalBars)
	startTS := int64(0)
	for i := 0; i < rampLen; i++ {
		p := 100.0 + float64(i)*100.0/float64(rampLen-1) // smooth 100 → 200
		bars[i] = domain.Bar{
			OpenTime: startTS + int64(i)*barInterval,
			Open:     p, High: p, Low: p, Close: p, Volume: 1,
		}
	}
	for i := rampLen; i < totalBars; i++ {
		bars[i] = domain.Bar{
			OpenTime: startTS + int64(i)*barInterval,
			Open:     200, High: 200, Low: 200, Close: 200, Volume: 1,
		}
	}

	mkWindow := func(name resultpkg.WindowName) domain.CrucibleWindow {
		return domain.CrucibleWindow{
			Name:      name,
			StartTS:   bars[0].OpenTime,
			EndTS:     bars[len(bars)-1].OpenTime,
			WarmupLen: warmup,
			Bars:      bars,
		}
	}
	return &domain.EvaluablePlan{
		Pair:    "BTCUSDT",
		Spawn:   resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		LotStep: 0.0001,
		LotMin:  0.001,
		Windows: []domain.CrucibleWindow{
			mkWindow(resultpkg.Window6M),
			mkWindow(resultpkg.Window2Y),
			mkWindow(resultpkg.Window5Y),
			mkWindow(resultpkg.Window10Y),
		},
		Friction: domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2},
	}
}

// TestGAEndToEndSigmoidV1 is the headline end-to-end integration
// test: a sigmoid_v1 strategy driven through the GA engine across a
// bullish synthetic series must produce a non-Fatal best gene
// scoring positively. Loose tolerance — we are validating the
// engine ↔ strategy wiring, not the strategy's optimality.
func TestGAEndToEndSigmoidV1(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 20
	cfg.MaxGenerations = 5
	cfg.EpochSeed = 1

	const barIntervalMs = int64(24) * 60 * 60 * 1000
	eng := engine.New(sigmoid.New(barIntervalMs), cfg)

	result, err := eng.RunEpoch(context.Background(), sigmoidPlanBullish())
	if err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if result.BestScore.Fatal {
		t.Fatalf("best Fatal on a smoothly bullish series — strategy broke (gene=%v)", result.BestGene)
	}
	if result.BestScore.Value == nil {
		t.Fatal("non-Fatal best has nil Value — engine broke")
	}
	// Sanity: a bullish ramp can be captured with cash → BTC weight.
	// We assert positive aggregate score; the precise number depends
	// on stochastic GA paths.
	if *result.BestScore.Value <= 0 {
		t.Errorf("best score = %v, want > 0 on a 100%% bullish ramp",
			*result.BestScore.Value)
	}
	t.Logf("end-to-end: score=%g fp=%s gene=%v",
		*result.BestScore.Value, result.BestFingerprint, result.BestGene)
}

// TestGAEndToEndSigmoidV1Deterministic mirrors the toy
// TestRunEpochDeterministic guarantee: two engine.RunEpoch calls
// with identical EpochSeed produce byte-equal best genes. The
// sigmoid_v1 strategy uses goroutine-parallel evaluation through
// the worker pool, but adapter.Evaluate is a pure function of
// (gene, plan), so per-index scores are stable regardless of
// worker pickup order.
func TestGAEndToEndSigmoidV1Deterministic(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 12
	cfg.MaxGenerations = 3
	cfg.EpochSeed = 99

	const barIntervalMs = int64(24) * 60 * 60 * 1000
	eng := engine.New(sigmoid.New(barIntervalMs), cfg)

	r1, err := eng.RunEpoch(context.Background(), sigmoidPlanBullish())
	if err != nil {
		t.Fatalf("first RunEpoch: %v", err)
	}
	r2, err := eng.RunEpoch(context.Background(), sigmoidPlanBullish())
	if err != nil {
		t.Fatalf("second RunEpoch: %v", err)
	}
	if !geneEqual(r1.BestGene, r2.BestGene) {
		t.Errorf("nondeterministic best gene:\n  r1=%v\n  r2=%v", r1.BestGene, r2.BestGene)
	}
	if r1.BestFingerprint != r2.BestFingerprint {
		t.Errorf("fingerprint differs: %s vs %s", r1.BestFingerprint, r2.BestFingerprint)
	}
}

// TestGAEndToEndSigmoidV1ProgressesGenerationOverGeneration asserts
// elitism + GA selection actually move the population forward:
// the best score recorded on the FINAL generation must be at
// least as good as the best score at generation 0. Anything less
// means elites are being lost — a regression in the engine, not
// the strategy.
func TestGAEndToEndSigmoidV1ProgressesGenerationOverGeneration(t *testing.T) {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 16
	cfg.MaxGenerations = 4
	cfg.EpochSeed = 7

	var (
		firstScore float64
		lastScore  float64
		haveFirst  bool
	)
	cfg.OnProgress = func(gen int, _ string, best resultpkg.ScoreTotal) {
		if best.Fatal || best.Value == nil {
			t.Errorf("gen=%d: best is Fatal or nil", gen)
			return
		}
		if !haveFirst {
			firstScore = *best.Value
			haveFirst = true
		}
		lastScore = *best.Value
	}

	const barIntervalMs = int64(24) * 60 * 60 * 1000
	eng := engine.New(sigmoid.New(barIntervalMs), cfg)
	if _, err := eng.RunEpoch(context.Background(), sigmoidPlanBullish()); err != nil {
		t.Fatalf("RunEpoch: %v", err)
	}
	if !haveFirst {
		t.Fatal("OnProgress never fired — engine never produced a best")
	}
	if lastScore < firstScore {
		t.Errorf("regression: gen0 best=%g but final best=%g (elite should never lose ground)",
			firstScore, lastScore)
	}
	t.Logf("progression: gen0=%g → final=%g (gain=%g)",
		firstScore, lastScore, lastScore-firstScore)
}
