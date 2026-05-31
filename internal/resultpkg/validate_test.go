package resultpkg

import (
	"encoding/json"
	"testing"
)

func f64(v float64) *float64      { return &v }
func sp(s string) *string         { return &s }
func skip(v SkippedBy) *SkippedBy { return &v }

// TestCrucibleResultValidate_ThreeStateMutex covers the three SliceScore
// states (normal / cascade-skipped / self-Fatal) and the illegal
// combinations between them. This is the §10.1 contract test the engine
// will rely on when materializing CrucibleResult instances.
func TestCrucibleResultValidate_ThreeStateMutex(t *testing.T) {
	cases := []struct {
		name    string
		in      CrucibleResult
		wantErr bool
	}{
		{
			name: "normal_ok",
			in: CrucibleResult{
				Window:        Window6M,
				Score:         SliceScore{Fatal: false, Value: f64(1.5)},
				BarsEvaluated: 1000,
			},
			wantErr: false,
		},
		{
			name: "cascade_skipped_ok",
			in: CrucibleResult{
				Window:        Window2Y,
				Score:         SliceScore{Fatal: false, Value: nil},
				SkippedBy:     skip(SkippedByCascadeFrom6M),
				BarsEvaluated: 0,
			},
			wantErr: false,
		},
		{
			name: "self_fatal_ok",
			in: CrucibleResult{
				Window:        Window6M,
				Score:         SliceScore{Fatal: true, Value: nil, Reason: sp("mdd_exceeded")},
				BarsEvaluated: 543,
				FatalReason:   sp("mdd_exceeded"),
			},
			wantErr: false,
		},
		{
			name: "fatal_invalid_reason",
			in: CrucibleResult{
				Window:        Window6M,
				Score:         SliceScore{Fatal: true, Value: nil},
				BarsEvaluated: 543,
				FatalReason:   sp("drawdown_0.70"),
			},
			wantErr: true,
		},
		{
			name: "normal_missing_value",
			in: CrucibleResult{
				Window:        Window6M,
				Score:         SliceScore{Fatal: false, Value: nil},
				BarsEvaluated: 100,
			},
			wantErr: true,
		},
		{
			name: "fatal_with_value",
			in: CrucibleResult{
				Window:        Window6M,
				Score:         SliceScore{Fatal: true, Value: f64(0.1)},
				BarsEvaluated: 100,
			},
			wantErr: true,
		},
		{
			name: "skipped_with_value",
			in: CrucibleResult{
				Window:        Window2Y,
				Score:         SliceScore{Fatal: false, Value: f64(0.5)},
				SkippedBy:     skip(SkippedByCascadeFrom6M),
				BarsEvaluated: 0,
			},
			wantErr: true,
		},
		{
			name: "fatal_and_skipped",
			in: CrucibleResult{
				Window:        Window2Y,
				Score:         SliceScore{Fatal: true, Value: nil},
				SkippedBy:     skip(SkippedByCascadeFrom6M),
				BarsEvaluated: 0,
			},
			wantErr: true,
		},
		{
			name: "invalid_window",
			in: CrucibleResult{
				Window:        "9y",
				Score:         SliceScore{Fatal: false, Value: f64(1.0)},
				BarsEvaluated: 1,
			},
			wantErr: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.in.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

// TestRawEvaluateResult_NoScoreTotalField verifies that the type
// physically lacks a ScoreTotal field — this is the architectural
// guard against strategies writing aggregate scores.
func TestRawEvaluateResult_NoScoreTotalField(t *testing.T) {
	b, err := json.Marshal(RawEvaluateResult{
		Windows:        []CrucibleResult{},
		FrictionActual: FrictionActual{TakerFeeBPS: 10, SlippageBPS: 5},
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A score_total key should never appear in the JSON.
	var m map[string]json.RawMessage
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal to map: %v", err)
	}
	if _, ok := m["score_total"]; ok {
		t.Errorf("RawEvaluateResult should not contain score_total; got %s", string(b))
	}
}

// TestChallengerResultPackage_RoundTrip verifies a fully populated
// package survives JSON marshalling and re-marshalling unchanged, and
// that Validate accepts it.
func TestChallengerResultPackage_RoundTrip(t *testing.T) {
	pkg := buildValidPackage()
	if err := pkg.Validate(); err != nil {
		t.Fatalf("Validate on freshly built package: %v", err)
	}
	b, err := json.Marshal(pkg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var rt ChallengerResultPackage
	if err := json.Unmarshal(b, &rt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if err := rt.Validate(); err != nil {
		t.Fatalf("Validate after round-trip: %v", err)
	}
	b2, err := json.Marshal(rt)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if string(b) != string(b2) {
		t.Errorf("round-trip diff:\n  first=%s\n  second=%s", string(b), string(b2))
	}
}

// TestChallengerResultPackage_VersionTripleMismatch verifies the cross-
// field consistency check catches a drift between Core and
// ReproducibilityMetadata version fields.
func TestChallengerResultPackage_VersionTripleMismatch(t *testing.T) {
	pkg := buildValidPackage()
	pkg.Core.ReproducibilityMetadata.SchemaVersion = "v5.3.2"
	if err := pkg.Validate(); err == nil {
		t.Fatal("expected version-triple mismatch error")
	}
}

func buildValidPackage() ChallengerResultPackage {
	return ChallengerResultPackage{
		Core: ResultCore{
			StrategyID: "demo-strat",
			ChampionGene: ChampionGenePayload{
				Encoding: GeneEncodingJSON,
				Payload:  json.RawMessage(`{"x":1}`),
			},
			SpawnPoint: SpawnPointPayload{SpawnMode: SpawnModeRandomOnce},
			ReproducibilityMetadata: ReproducibilityMetadata{
				EpochSeed:          42,
				DataVersion:        "binance.vision@2026-01",
				EngineVersion:      "v0.1.0",
				StrategyVersion:    "v0.1.0",
				SchemaVersion:      SchemaVersionV533,
				FitnessVersion:     FitnessVersionV1RawStd,
				FingerprintVersion: FingerprintVersionV1,
				HardwareSignature:  "linux/amd64/test",
				GoVersion:          "go1.23",
				BuildID:            "test-build",
				PlanHash:           "0000000000000000000000000000000000000000000000000000000000000000",
				BarsHash:           "1111111111111111111111111111111111111111111111111111111111111111",
			},
			GAConfig: GAConfigSnapshot{
				StrategyID: "demo-strat", Pair: "BTCUSDT",
				PopSize: 200, MaxGenerations: 30,
				EliteRatio: 0.05, FatalMDD: 0.70,
				TakerFeeBPS: 10, SlippageBPS: 5,
				SpawnMode: SpawnModeRandomOnce, TestMode: false,
			},
			SchemaVersion:      SchemaVersionV533,
			FitnessVersion:     FitnessVersionV1RawStd,
			FingerprintVersion: FingerprintVersionV1,
		},
		Evaluation: EvaluationLayer{
			WindowScores: []CrucibleResult{
				{Window: Window6M, Score: SliceScore{Fatal: false, Value: f64(1.0)}, BarsEvaluated: 1000},
			},
			ScoreTotal:     ScoreTotal{Fatal: false, Value: f64(1.0)},
			FrictionActual: FrictionActual{TakerFeeBPS: 10, SlippageBPS: 5},
		},
		Verification: VerificationLayer{
			OOSResult: OOSResult{Status: VerificationStatusNotRun},
		},
		Promote: PromoteLayer{DecisionStatus: DecisionStatusPending},
	}
}
