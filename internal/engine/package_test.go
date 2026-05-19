package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"math/rand"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
	"quantlab/internal/strategy"
)

// fakeEncodeStrategy is a minimal EvolvableStrategy used to test the
// BuildChallengerPackage flow without pulling sigmoid_v1 / toy into
// the engine_test scope (those tests import engine, so importing
// them back here would create a cycle).
type fakeEncodeStrategy struct {
	id string
}

func (f *fakeEncodeStrategy) StrategyID() string                 { return f.id }
func (f *fakeEncodeStrategy) Segments() []domain.SegmentInfo     { return nil }
func (f *fakeEncodeStrategy) Sample(_ *rand.Rand) domain.Gene    { return nil }
func (f *fakeEncodeStrategy) Clamp(g domain.Gene) domain.Gene    { return g }
func (f *fakeEncodeStrategy) Validate(_ domain.Gene) error       { return nil }
func (f *fakeEncodeStrategy) Crossover(p1, _ domain.Gene, _ *rand.Rand) domain.Gene {
	return p1
}
func (f *fakeEncodeStrategy) Mutate(g domain.Gene, _, _ float64, _ *rand.Rand) domain.Gene {
	return g
}
func (f *fakeEncodeStrategy) Fingerprint(_ domain.Gene) string { return "fp-fake" }
func (f *fakeEncodeStrategy) MinEvalBars() int                 { return 1 }
func (f *fakeEncodeStrategy) NewAdapter(_ *domain.EvaluablePlan) (strategy.Adapter, error) {
	return nil, nil
}
func (f *fakeEncodeStrategy) Evaluate(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.RawEvaluateResult, error) {
	return nil, nil
}
func (f *fakeEncodeStrategy) ReviewBacktest(_ context.Context, _ domain.Gene, _ *domain.EvaluablePlan) (*resultpkg.ReviewSummary, error) {
	return nil, nil
}
func (f *fakeEncodeStrategy) DecodeElite(_ resultpkg.ChampionGenePayload) (domain.Gene, error) {
	return nil, nil
}

// EncodeResult mirrors toy.EncodeResult's pattern: stamp StrategyID,
// init Promote to pending, copy engine-supplied layers verbatim.
// The bestGene is stored as the raw JSON of the float slice (matches
// resultpkg.GeneEncodingJSON contract).
func (f *fakeEncodeStrategy) EncodeResult(
	gene domain.Gene,
	spawn resultpkg.SpawnPointPayload,
	repro resultpkg.ReproducibilityMetadata,
	gaConfig resultpkg.GAConfigSnapshot,
	eval *resultpkg.EvaluationLayer,
	verif *resultpkg.VerificationLayer,
	diag *resultpkg.DiagnosticsLayer,
) (resultpkg.ChallengerResultPackage, error) {
	// Inline the minimal "marshal gene + populate core" path.
	payload := []byte("[]") // fake — keeps the test self-contained
	if len(gene) > 0 {
		payload = []byte("[fake-gene]")
	}
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID: f.StrategyID(),
			ChampionGene: resultpkg.ChampionGenePayload{
				Encoding: resultpkg.GeneEncodingJSON,
				Payload:  payload,
			},
			SpawnPoint:              spawn,
			ReproducibilityMetadata: repro,
			GAConfig:                gaConfig,
			SchemaVersion:           repro.SchemaVersion,
			FitnessVersion:          repro.FitnessVersion,
			FingerprintVersion:      repro.FingerprintVersion,
		},
		Promote: resultpkg.PromoteLayer{DecisionStatus: resultpkg.DecisionStatusPending},
	}
	if eval != nil {
		pkg.Evaluation = *eval
	}
	if verif != nil {
		pkg.Verification = *verif
	}
	if diag != nil {
		pkg.Diagnostics = *diag
	}
	return pkg, nil
}

// ----- tests -----

func TestBuildChallengerPackage_PopulatesCore(t *testing.T) {
	score := 1.5
	scoreRaw := 1.7
	cons := 0.2

	raw := &resultpkg.RawEvaluateResult{
		Windows: []resultpkg.CrucibleResult{{
			Window:        resultpkg.Window6M,
			Score:         resultpkg.SliceScore{Fatal: false, Value: &score},
			BarsEvaluated: 100,
		}},
		FrictionActual: resultpkg.FrictionActual{TakerFeeBPS: 5, SlippageBPS: 2},
		BarsEvaluated:  100,
	}
	st := resultpkg.ScoreTotal{
		Fatal: false, Value: &score, ScoreRaw: &scoreRaw, ConsistencyPenalty: &cons,
	}

	cfg := DefaultConfig()
	cfg.PopSize = 20
	cfg.MaxGenerations = 10
	cfg.EpochSeed = 99

	plan := &domain.EvaluablePlan{
		Pair:     "BTCUSDT",
		Spawn:    resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
		FatalMDD: 0.5,
		Friction: domain.FrictionParams{TakerFeeBPS: 5, SlippageBPS: 2},
	}
	bc := BuildContext{
		ChallengerID:      "test-001",
		Pair:              "BTCUSDT",
		DataVersion:       "binance/v1",
		EngineVersion:     "engine-test",
		StrategyVersion:   "fake-strat",
		HardwareSignature: "linux/amd64/test",
		GoVersion:         "go1.23",
		BuildID:           "build-x",
		PlanHash:          "deadbeef",
		BarsHash:          "cafef00d",
	}

	pkg, err := BuildChallengerPackage(
		&fakeEncodeStrategy{id: "fake"},
		plan,
		domain.Gene{0.5},
		raw, st, cfg, bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage: %v", err)
	}

	// Core stamped by EncodeResult.
	if pkg.Core.StrategyID != "fake" {
		t.Errorf("StrategyID = %q, want fake", pkg.Core.StrategyID)
	}
	if pkg.Promote.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("DecisionStatus = %q, want pending", pkg.Promote.DecisionStatus)
	}
	// Repro mirrored from BuildContext.
	if pkg.Core.ReproducibilityMetadata.EpochSeed != 99 {
		t.Errorf("EpochSeed = %d, want 99", pkg.Core.ReproducibilityMetadata.EpochSeed)
	}
	if pkg.Core.ReproducibilityMetadata.PlanHash != "deadbeef" {
		t.Errorf("PlanHash = %q, want deadbeef", pkg.Core.ReproducibilityMetadata.PlanHash)
	}
	if pkg.Core.ReproducibilityMetadata.SchemaVersion != resultpkg.SchemaVersionV533 {
		t.Errorf("SchemaVersion = %q, want %q",
			pkg.Core.ReproducibilityMetadata.SchemaVersion, resultpkg.SchemaVersionV533)
	}
	// GAConfig mirrors EngineConfig + plan.Friction.
	if pkg.Core.GAConfig.PopSize != 20 {
		t.Errorf("GAConfig.PopSize = %d, want 20", pkg.Core.GAConfig.PopSize)
	}
	if pkg.Core.GAConfig.TakerFeeBPS != 5 {
		t.Errorf("GAConfig.TakerFeeBPS = %v, want 5", pkg.Core.GAConfig.TakerFeeBPS)
	}
	// Evaluation layer copied from raw + score.
	if pkg.Evaluation.ScoreTotal.Value == nil || *pkg.Evaluation.ScoreTotal.Value != 1.5 {
		t.Errorf("Evaluation.ScoreTotal.Value mismatch: %+v", pkg.Evaluation.ScoreTotal)
	}
	if len(pkg.Evaluation.WindowScores) != 1 {
		t.Errorf("WindowScores len = %d, want 1", len(pkg.Evaluation.WindowScores))
	}
	if pkg.Evaluation.FrictionActual.TakerFeeBPS != 5 {
		t.Errorf("FrictionActual mismatch: %+v", pkg.Evaluation.FrictionActual)
	}
}

func TestBuildChallengerPackage_TestModeFlagsThroughGAConfig(t *testing.T) {
	score := 1.0
	raw := &resultpkg.RawEvaluateResult{
		Windows:        []resultpkg.CrucibleResult{{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Value: &score}}},
		FrictionActual: resultpkg.FrictionActual{},
	}
	st := resultpkg.ScoreTotal{Value: &score}

	plan := &domain.EvaluablePlan{FatalMDD: 0.5, Friction: domain.FrictionParams{}}
	bc := BuildContext{TestMode: true, Pair: "BTCUSDT"}

	pkg, err := BuildChallengerPackage(
		&fakeEncodeStrategy{id: "fake"},
		plan,
		domain.Gene{0.1},
		raw, st, DefaultConfig(), bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage: %v", err)
	}
	if !pkg.Core.GAConfig.TestMode {
		t.Errorf("TestMode did not propagate to GAConfig: %+v", pkg.Core.GAConfig)
	}
}

// TestBuildChallengerPackage_DSRSummaryPropagates pins the
// BuildContext.DSRSummary → VerificationLayer.DSRSummary edge that
// the SaaS Epoch service uses after SharpeBank crosses
// MinTrialsForDSR. Empty BC.DSRSummary leaves verif.DSRSummary unset.
func TestBuildChallengerPackage_DSRSummaryPropagates(t *testing.T) {
	score := 1.0
	raw := &resultpkg.RawEvaluateResult{
		Windows:        []resultpkg.CrucibleResult{{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Value: &score}}},
		FrictionActual: resultpkg.FrictionActual{},
	}
	st := resultpkg.ScoreTotal{Value: &score}
	plan := &domain.EvaluablePlan{FatalMDD: 0.5, Friction: domain.FrictionParams{}}

	payload := json.RawMessage(`{"dsr":0.73,"observed_sharpe":0.42}`)
	bc := BuildContext{Pair: "BTCUSDT", DSRSummary: payload}

	pkg, err := BuildChallengerPackage(
		&fakeEncodeStrategy{id: "fake"},
		plan, domain.Gene{0.1}, raw, st, DefaultConfig(), bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage: %v", err)
	}
	if !bytes.Equal(pkg.Verification.DSRSummary, payload) {
		t.Errorf("Verification.DSRSummary = %s, want %s",
			pkg.Verification.DSRSummary, payload)
	}

	// Empty input ⇒ field stays unset.
	bc.DSRSummary = nil
	pkg2, err := BuildChallengerPackage(
		&fakeEncodeStrategy{id: "fake"},
		plan, domain.Gene{0.1}, raw, st, DefaultConfig(), bc,
	)
	if err != nil {
		t.Fatalf("BuildChallengerPackage (empty DSR): %v", err)
	}
	if len(pkg2.Verification.DSRSummary) != 0 {
		t.Errorf("empty BC.DSRSummary should leave field unset, got %s",
			pkg2.Verification.DSRSummary)
	}
}
