package sigmoid_v1

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	"quantlab/internal/domain"
	"quantlab/internal/resultpkg"
)

// reproFixture returns a ReproducibilityMetadata with the prototype
// version constants populated. Tests pass this through EncodeResult
// where the same values get mirrored into Core.{SchemaVersion,...}.
func reproFixture() resultpkg.ReproducibilityMetadata {
	return resultpkg.ReproducibilityMetadata{
		EpochSeed:          12345,
		DataVersion:        "binance/v1",
		EngineVersion:      "test-engine",
		StrategyVersion:    "sigmoid_v1",
		SchemaVersion:      "v5.3.3",
		FitnessVersion:     "v1-raw-std",
		FingerprintVersion: "fp-v1",
		HardwareSignature:  "linux/amd64/test",
		GoVersion:          "go1.22",
		BuildID:            "test-build",
		PlanHash:           "deadbeef",
		BarsHash:           "cafef00d",
	}
}

func gaConfigFixture() resultpkg.GAConfigSnapshot {
	return resultpkg.GAConfigSnapshot{
		StrategyID:     "sigmoid_v1",
		Pair:           "BTCUSDT",
		PopSize:        32,
		MaxGenerations: 10,
		EliteRatio:     0.1,
		FatalMDD:       0.5,
		TakerFeeBPS:    5,
		SlippageBPS:    2,
		SpawnMode:      resultpkg.SpawnModeInherit,
		TestMode:       false,
	}
}

func TestEncodeResultRoundTrip(t *testing.T) {
	s := stepTestSigmoid()
	gene := EncodeChromosome(defaultChromosome())

	pkg, err := s.EncodeResult(
		gene,
		resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeInherit},
		reproFixture(),
		gaConfigFixture(),
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("EncodeResult: %v", err)
	}

	got, err := s.DecodeElite(pkg.Core.ChampionGene)
	if err != nil {
		t.Fatalf("DecodeElite: %v", err)
	}
	if !reflect.DeepEqual(got, gene) {
		t.Errorf("round-trip mismatch:\n  got  %v\n  want %v", got, gene)
	}
}

func TestEncodeResultPopulatesCore(t *testing.T) {
	s := stepTestSigmoid()
	gene := EncodeChromosome(defaultChromosome())
	pkg, err := s.EncodeResult(
		gene,
		resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeInherit},
		reproFixture(),
		gaConfigFixture(),
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("EncodeResult: %v", err)
	}

	core := pkg.Core
	if core.StrategyID != StrategyID {
		t.Errorf("StrategyID = %q, want %q", core.StrategyID, StrategyID)
	}
	if core.ChampionGene.Encoding != resultpkg.GeneEncodingJSON {
		t.Errorf("ChampionGene.Encoding = %q, want %q",
			core.ChampionGene.Encoding, resultpkg.GeneEncodingJSON)
	}
	// Version fields must mirror ReproducibilityMetadata (validate.go
	// checks cross-field equality).
	repro := reproFixture()
	if core.SchemaVersion != repro.SchemaVersion {
		t.Errorf("SchemaVersion = %q, want %q (mirrored from repro)",
			core.SchemaVersion, repro.SchemaVersion)
	}
	if core.FitnessVersion != repro.FitnessVersion {
		t.Errorf("FitnessVersion = %q, want %q", core.FitnessVersion, repro.FitnessVersion)
	}
	if core.FingerprintVersion != repro.FingerprintVersion {
		t.Errorf("FingerprintVersion = %q, want %q",
			core.FingerprintVersion, repro.FingerprintVersion)
	}
	// Promote layer must start at pending.
	if pkg.Promote.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("DecisionStatus = %q, want %q",
			pkg.Promote.DecisionStatus, resultpkg.DecisionStatusPending)
	}
}

func TestEncodeResultMergesLayers(t *testing.T) {
	s := stepTestSigmoid()
	eval := &resultpkg.EvaluationLayer{
		FrictionActual: resultpkg.FrictionActual{TakerFeeBPS: 7},
	}
	verif := &resultpkg.VerificationLayer{}
	diag := &resultpkg.DiagnosticsLayer{}

	pkg, err := s.EncodeResult(
		EncodeChromosome(defaultChromosome()),
		resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeInherit},
		reproFixture(),
		gaConfigFixture(),
		eval, verif, diag,
	)
	if err != nil {
		t.Fatalf("EncodeResult: %v", err)
	}
	if pkg.Evaluation.FrictionActual.TakerFeeBPS != 7 {
		t.Errorf("eval layer not merged: %+v", pkg.Evaluation)
	}
}

func TestEncodeResultPackagePassesValidate(t *testing.T) {
	// Generated package must satisfy resultpkg.Validate's cross-field
	// checks (gene encoding, version equality, etc.). This is the
	// closest thing we have to an integration-level guarantee that
	// EncodeResult's output is consumable downstream.
	s := stepTestSigmoid()
	pkg, err := s.EncodeResult(
		EncodeChromosome(defaultChromosome()),
		resultpkg.SpawnPointPayload{SpawnMode: resultpkg.SpawnModeInherit},
		reproFixture(),
		gaConfigFixture(),
		nil, nil, nil,
	)
	if err != nil {
		t.Fatalf("EncodeResult: %v", err)
	}
	if err := pkg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestDecodeEliteRejectsBadEncoding(t *testing.T) {
	s := stepTestSigmoid()
	gene := EncodeChromosome(defaultChromosome())
	payload, _ := json.Marshal(gene)
	_, err := s.DecodeElite(resultpkg.ChampionGenePayload{
		Encoding: "msgpack",
		Payload:  payload,
	})
	if err == nil {
		t.Error("DecodeElite with non-JSON encoding: want error, got nil")
	}
}

func TestDecodeEliteRejectsMalformedJSON(t *testing.T) {
	s := stepTestSigmoid()
	_, err := s.DecodeElite(resultpkg.ChampionGenePayload{
		Encoding: resultpkg.GeneEncodingJSON,
		Payload:  json.RawMessage("not-json"),
	})
	if err == nil {
		t.Error("DecodeElite with malformed JSON: want error, got nil")
	}
}

func TestDecodeEliteRejectsInvalidGene(t *testing.T) {
	// JSON parses fine but produces an out-of-bounds gene that fails
	// Validate. The strategy must catch this before handing the gene
	// back as a Champion.
	s := stepTestSigmoid()
	// Out-of-bounds A1 = 99 (legal range [-1, 1]).
	bad := EncodeChromosome(defaultChromosome())
	bad[geneDimA1] = 99
	payload, _ := json.Marshal(bad)

	_, err := s.DecodeElite(resultpkg.ChampionGenePayload{
		Encoding: resultpkg.GeneEncodingJSON,
		Payload:  payload,
	})
	if err == nil {
		t.Error("DecodeElite with invalid gene: want error, got nil")
	}
}

func TestDecodeEliteRejectsWrongDimension(t *testing.T) {
	// JSON parses but produces a 5-dim Gene (we expect 13).
	s := stepTestSigmoid()
	short := domain.Gene{0.1, 0.2, 0.3, 0.4, 0.5}
	payload, _ := json.Marshal(short)
	_, err := s.DecodeElite(resultpkg.ChampionGenePayload{
		Encoding: resultpkg.GeneEncodingJSON,
		Payload:  payload,
	})
	if err == nil {
		t.Error("DecodeElite with short gene: want error, got nil")
	}
}

func TestReviewBacktestNoOp(t *testing.T) {
	// Prototype contract: (nil, nil). Engine must accept both
	// representations of "no review yet" equivalently.
	s := stepTestSigmoid()
	got, err := s.ReviewBacktest(context.Background(), EncodeChromosome(defaultChromosome()), nil)
	if err != nil {
		t.Errorf("ReviewBacktest err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("ReviewBacktest = %+v, want nil (prototype no-op)", got)
	}
}
