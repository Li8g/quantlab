package repository

import (
	"encoding/json"
	"testing"

	"quantlab/internal/resultpkg"
)

// samplePackage returns a fully-populated ChallengerResultPackage used
// across the decomposer tests. All optional pointers carry non-nil
// values so the lift-into-columns paths get exercised.
func samplePackage() resultpkg.ChallengerResultPackage {
	score := 1.25
	scoreRaw := 1.40
	cons := 0.15
	oosM := 0.05
	oosW := 0.03
	note := "decision pending"
	reviewer := "alice@quantlab"
	reviewedAt := int64(1_700_000_000_000)

	return resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID: "sigmoid_v1",
			ChampionGene: resultpkg.ChampionGenePayload{
				Encoding: resultpkg.GeneEncodingJSON,
				Payload:  json.RawMessage(`[0.1,0.2,0.3]`),
			},
			SpawnPoint:    resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeRandomOnce},
			ReproducibilityMetadata: resultpkg.ReproducibilityMetadata{
				EpochSeed:          42,
				DataVersion:        "binance/v1",
				EngineVersion:      "engine-test",
				StrategyVersion:    "sigmoid_v1.0",
				SchemaVersion:      resultpkg.SchemaVersionV533,
				FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
				FingerprintVersion: resultpkg.FingerprintVersionV1,
				HardwareSignature:  "linux/amd64/Intel",
				GoVersion:          "go1.23",
				BuildID:            "build-001",
				PlanHash:           "deadbeef00000000",
				BarsHash:           "cafef00d00000000",
			},
			GAConfig: resultpkg.GAConfigSnapshot{
				StrategyID:     "sigmoid_v1",
				Pair:           "BTCUSDT",
				PopSize:        32,
				MaxGenerations: 10,
				EliteRatio:     0.05,
				FatalMDD:       0.5,
				TakerFeeBPS:    5,
				SlippageBPS:    2,
				SpawnMode:      resultpkg.SpawnModeRandomOnce,
				TestMode:       false,
			},
			SchemaVersion:      resultpkg.SchemaVersionV533,
			FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
			FingerprintVersion: resultpkg.FingerprintVersionV1,
		},
		Evaluation: resultpkg.EvaluationLayer{
			WindowScores: []resultpkg.CrucibleResult{
				{
					Window:        resultpkg.Window6M,
					Score:         resultpkg.SliceScore{Fatal: false, Value: &score},
					BarsEvaluated: 100,
				},
			},
			ScoreTotal: resultpkg.ScoreTotal{
				Fatal:              false,
				Value:              &score,
				ScoreRaw:           &scoreRaw,
				ConsistencyPenalty: &cons,
			},
			FrictionActual: resultpkg.FrictionActual{TakerFeeBPS: 5, SlippageBPS: 2},
		},
		Verification: resultpkg.VerificationLayer{
			OOSResult: resultpkg.OOSResult{
				Status:          resultpkg.VerificationStatusOK,
				OOSAlphaMonthly: &oosM,
				OOSAlphaWeekly:  &oosW,
			},
		},
		Promote: resultpkg.PromoteLayer{
			DecisionStatus: resultpkg.DecisionStatusPending,
			DecisionNote:   &note,
			ReviewedAtTS:   &reviewedAt,
			ReviewedBy:     &reviewer,
		},
	}
}

func TestBuildGeneRecord_MirrorsCoreFields(t *testing.T) {
	pkg := samplePackage()
	rec, err := buildGeneRecord("ch-001", pkg)
	if err != nil {
		t.Fatalf("buildGeneRecord: %v", err)
	}
	if rec.ChallengerID != "ch-001" {
		t.Errorf("ChallengerID = %q, want ch-001", rec.ChallengerID)
	}
	if rec.StrategyID != "sigmoid_v1" {
		t.Errorf("StrategyID = %q", rec.StrategyID)
	}
	if rec.Pair != "BTCUSDT" {
		t.Errorf("Pair = %q", rec.Pair)
	}
	if rec.EpochSeed != 42 {
		t.Errorf("EpochSeed = %d", rec.EpochSeed)
	}
	if rec.PlanHash != "deadbeef00000000" || rec.BarsHash != "cafef00d00000000" {
		t.Errorf("plan/bars hash mismatch: %q / %q", rec.PlanHash, rec.BarsHash)
	}
	if rec.SchemaVersion != resultpkg.SchemaVersionV533 {
		t.Errorf("SchemaVersion = %q", rec.SchemaVersion)
	}
}

func TestBuildGeneRecord_LiftsScoreTotal(t *testing.T) {
	pkg := samplePackage()
	rec, _ := buildGeneRecord("ch-002", pkg)

	if rec.ScoreTotal == nil || *rec.ScoreTotal != 1.25 {
		t.Errorf("ScoreTotal lift mismatch: %v", rec.ScoreTotal)
	}
	if rec.ScoreRaw == nil || *rec.ScoreRaw != 1.40 {
		t.Errorf("ScoreRaw lift mismatch: %v", rec.ScoreRaw)
	}
	if rec.ConsistencyPenalty == nil || *rec.ConsistencyPenalty != 0.15 {
		t.Errorf("ConsistencyPenalty lift mismatch: %v", rec.ConsistencyPenalty)
	}
}

func TestBuildGeneRecord_LiftsOOSAlphas(t *testing.T) {
	pkg := samplePackage()
	rec, _ := buildGeneRecord("ch-003", pkg)

	if rec.OosAlphaMonthly == nil || *rec.OosAlphaMonthly != 0.05 {
		t.Errorf("OosAlphaMonthly lift mismatch: %v", rec.OosAlphaMonthly)
	}
	if rec.OosAlphaWeekly == nil || *rec.OosAlphaWeekly != 0.03 {
		t.Errorf("OosAlphaWeekly lift mismatch: %v", rec.OosAlphaWeekly)
	}
}

func TestBuildGeneRecord_LiftsPromoteFields(t *testing.T) {
	pkg := samplePackage()
	rec, _ := buildGeneRecord("ch-004", pkg)

	if rec.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("DecisionStatus = %q, want pending", rec.DecisionStatus)
	}
	if rec.DecisionNote == nil || *rec.DecisionNote != "decision pending" {
		t.Errorf("DecisionNote lift mismatch: %v", rec.DecisionNote)
	}
	if rec.ReviewedBy == nil || *rec.ReviewedBy != "alice@quantlab" {
		t.Errorf("ReviewedBy lift mismatch: %v", rec.ReviewedBy)
	}
	if rec.ReviewedAtTS == nil || *rec.ReviewedAtTS != 1_700_000_000_000 {
		t.Errorf("ReviewedAtTS lift mismatch: %v", rec.ReviewedAtTS)
	}
}

func TestBuildGeneRecord_FullPackageJSONRoundTrips(t *testing.T) {
	pkg := samplePackage()
	rec, err := buildGeneRecord("ch-005", pkg)
	if err != nil {
		t.Fatalf("buildGeneRecord: %v", err)
	}
	// Source-of-truth contract: FullPackageJSON unmarshals back to an
	// equivalent ChallengerResultPackage. Validate() is the strongest
	// shape check we have — its cross-field equality rules (schema /
	// fitness / fingerprint versions, gene encoding) hold iff the
	// blob faithfully captures the package.
	var roundTrip resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(rec.FullPackageJSON, &roundTrip); err != nil {
		t.Fatalf("unmarshal FullPackageJSON: %v", err)
	}
	if err := roundTrip.Validate(); err != nil {
		t.Errorf("Validate after round-trip: %v", err)
	}
	if roundTrip.Core.StrategyID != pkg.Core.StrategyID {
		t.Errorf("round-trip StrategyID mismatch: %q vs %q",
			roundTrip.Core.StrategyID, pkg.Core.StrategyID)
	}
}

func TestBuildGeneRecord_WindowScoresJSONNonEmpty(t *testing.T) {
	pkg := samplePackage()
	rec, _ := buildGeneRecord("ch-006", pkg)
	if len(rec.WindowScoresJSON) == 0 {
		t.Error("WindowScoresJSON is empty")
	}
	// Sanity: should parse as a JSON array.
	var v []resultpkg.CrucibleResult
	if err := json.Unmarshal(rec.WindowScoresJSON, &v); err != nil {
		t.Errorf("WindowScoresJSON unparseable: %v", err)
	}
	if len(v) != 1 {
		t.Errorf("WindowScoresJSON: got %d entries, want 1", len(v))
	}
}

func TestBuildGeneRecord_FatalScoreLeavesValueNil(t *testing.T) {
	pkg := samplePackage()
	pkg.Evaluation.ScoreTotal = resultpkg.ScoreTotal{Fatal: true} // wipe Value/ScoreRaw/Cons
	pkg.Evaluation.WindowScores = []resultpkg.CrucibleResult{
		{Window: resultpkg.Window6M, Score: resultpkg.SliceScore{Fatal: true}},
	}

	rec, err := buildGeneRecord("ch-007", pkg)
	if err != nil {
		t.Fatalf("buildGeneRecord: %v", err)
	}
	if rec.ScoreTotal != nil {
		t.Errorf("Fatal SliceScore: ScoreTotal column should be nil, got %v", *rec.ScoreTotal)
	}
	if rec.ScoreRaw != nil || rec.ConsistencyPenalty != nil {
		t.Errorf("Fatal: ScoreRaw/Consistency should be nil, got %v / %v",
			rec.ScoreRaw, rec.ConsistencyPenalty)
	}
}
