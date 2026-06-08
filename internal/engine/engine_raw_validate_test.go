package engine_test

import (
	"context"
	"math/rand"
	"strings"
	"sync/atomic"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/engine"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// nilRawStrat is a minimal EvolvableStrategy whose adapter always returns
// (nil, nil) from Evaluate. Used to verify that evaluatePopulation fails
// closed instead of panicking on a nil RawEvaluateResult.
type nilRawStrat struct{}

func (s *nilRawStrat) StrategyID() string { return "stub-nil-raw" }
func (s *nilRawStrat) MinEvalBars() int   { return 1 }
func (s *nilRawStrat) Segments() []domain.SegmentInfo {
	return []domain.SegmentInfo{{Name: "x", Dimensions: []int{0}, GeneStep: []float64{0.1}}}
}
func (s *nilRawStrat) Sample(rng *rand.Rand) domain.Gene      { return domain.Gene{rng.Float64()} }
func (s *nilRawStrat) Clamp(g domain.Gene) domain.Gene        { return g }
func (s *nilRawStrat) Validate(g domain.Gene) error           { return nil }
func (s *nilRawStrat) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene { return p1 }
func (s *nilRawStrat) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene {
	return g
}
func (s *nilRawStrat) Fingerprint(_ domain.Gene) string { return "fp" }
func (s *nilRawStrat) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	return nil, nil
}
func (s *nilRawStrat) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (s *nilRawStrat) EncodeResult(_ domain.Gene, _ resultpkg.SpawnPointPayload, _ resultpkg.ReproducibilityMetadata, _ resultpkg.GAConfigSnapshot, _ *resultpkg.EvaluationLayer, _ *resultpkg.VerificationLayer, _ *resultpkg.DiagnosticsLayer) (resultpkg.ChallengerResultPackage, error) {
	return resultpkg.ChallengerResultPackage{}, nil
}
func (s *nilRawStrat) DecodeElite(_ resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return domain.Gene{0}, nil
}
func (s *nilRawStrat) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	return &nilRawAdapter{}, nil
}

type nilRawAdapter struct{}

func (a *nilRawAdapter) Reset(_ *domain.EvaluablePlan) error                    { return nil }
func (a *nilRawAdapter) Evaluate(_ domain.Gene) (*resultpkg.RawEvaluateResult, error) {
	return nil, nil
}
func (a *nilRawAdapter) Close() error { return nil }

// countingStrat's adapter returns valid IS raw for the first callLimit calls,
// then nil — used to let the hot loop succeed while making the best-gene
// re-evaluation return nil.
type countingStrat struct {
	calls     atomic.Int64
	callLimit int64
}

func (s *countingStrat) StrategyID() string { return "stub-counting" }
func (s *countingStrat) MinEvalBars() int   { return 1 }
func (s *countingStrat) Segments() []domain.SegmentInfo {
	return []domain.SegmentInfo{{Name: "x", Dimensions: []int{0}, GeneStep: []float64{0.1}}}
}
func (s *countingStrat) Sample(rng *rand.Rand) domain.Gene      { return domain.Gene{rng.Float64()} }
func (s *countingStrat) Clamp(g domain.Gene) domain.Gene        { return g }
func (s *countingStrat) Validate(g domain.Gene) error           { return nil }
func (s *countingStrat) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene { return p1 }
func (s *countingStrat) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene {
	return g
}
func (s *countingStrat) Fingerprint(_ domain.Gene) string { return "fp" }
func (s *countingStrat) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	return nil, nil
}
func (s *countingStrat) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (s *countingStrat) EncodeResult(_ domain.Gene, _ resultpkg.SpawnPointPayload, _ resultpkg.ReproducibilityMetadata, _ resultpkg.GAConfigSnapshot, _ *resultpkg.EvaluationLayer, _ *resultpkg.VerificationLayer, _ *resultpkg.DiagnosticsLayer) (resultpkg.ChallengerResultPackage, error) {
	return resultpkg.ChallengerResultPackage{}, nil
}
func (s *countingStrat) DecodeElite(_ resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return domain.Gene{0}, nil
}
func (s *countingStrat) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	return &countingAdapter{parent: s}, nil
}

type countingAdapter struct{ parent *countingStrat }

func (a *countingAdapter) Reset(_ *domain.EvaluablePlan) error { return nil }
func (a *countingAdapter) Close() error                        { return nil }
func (a *countingAdapter) Evaluate(_ domain.Gene) (*resultpkg.RawEvaluateResult, error) {
	n := a.parent.calls.Add(1)
	if n <= a.parent.callLimit {
		v := 1.0
		return &resultpkg.RawEvaluateResult{
			Windows: []resultpkg.CrucibleResult{
				{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Value: &v}, BarsEvaluated: 1},
			},
		}, nil
	}
	return nil, nil
}

// miniPlanForStub builds a plan accepted by stubs that declare MinEvalBars=1.
func miniPlanForStub() *domain.EvaluablePlan {
	windows := make([]domain.CrucibleWindow, 0, 4)
	for _, name := range resultpkg.AllWindowsInEvalOrder() {
		windows = append(windows, domain.CrucibleWindow{
			Name: name,
			Bars: []domain.Bar{{OpenTime: 1, Close: 100}},
		})
	}
	return &domain.EvaluablePlan{
		Pair:        "BTCUSDT",
		Spawn:       resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		LotStep:     0.0001,
		LotMin:      0.001,
		FatalMDD:    0.5,
		InitialUSDT: 10_000,
		Windows:     windows,
		Friction:    domain.FrictionParams{TakerFeeBPS: 10, SlippageBPS: 5},
	}
}

func stubConfig() engine.EngineConfig {
	cfg := engine.DefaultConfig()
	cfg.PopSize = 2
	cfg.MaxGenerations = 1
	cfg.EpochSeed = 1
	return cfg
}

// TestRunEpoch_NilRawReturnsError verifies that evaluatePopulation fails
// closed (returns error) instead of panicking when adapter.Evaluate returns
// (nil, nil). This pins the ValidateForIS guard in the hot loop.
func TestRunEpoch_NilRawReturnsError(t *testing.T) {
	eng := engine.New(&nilRawStrat{}, stubConfig())
	_, err := eng.RunEpoch(context.Background(), miniPlanForStub())
	if err == nil {
		t.Fatal("expected error from nil raw, got nil")
	}
	if !strings.Contains(err.Error(), "invalid raw") {
		t.Errorf("error %q should contain 'invalid raw'", err.Error())
	}
}

// TestRunEpoch_BestReEvalNilRawReturnsError verifies that the best-gene
// re-evaluation path also fails closed when adapter.Evaluate returns nil.
// The countingStrat returns valid raw for the hot loop (callLimit=PopSize)
// and nil on the subsequent best-gene re-eval call.
func TestRunEpoch_BestReEvalNilRawReturnsError(t *testing.T) {
	strat := &countingStrat{callLimit: 2} // PopSize=2 hot-loop calls succeed
	eng := engine.New(strat, stubConfig())
	_, err := eng.RunEpoch(context.Background(), miniPlanForStub())
	if err == nil {
		t.Fatal("expected error from nil raw on re-eval, got nil")
	}
	if !strings.Contains(err.Error(), "invalid raw") {
		t.Errorf("error %q should contain 'invalid raw'", err.Error())
	}
}
