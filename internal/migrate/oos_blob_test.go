package migrate

import (
	"encoding/json"
	"strings"
	"testing"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// preFiveDBlob mimics a row written before Phase 5D — Verification
// layer present but OOSResult.Status is the empty string (or
// equivalently absent on older engines).
func preFiveDBlob(t *testing.T) []byte {
	t.Helper()
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID:         "sigmoid_v1",
			SchemaVersion:      resultpkg.SchemaVersionV533,
			FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
			FingerprintVersion: resultpkg.FingerprintVersionV1,
			ChampionGene: resultpkg.ChampionGenePayload{
				Encoding: resultpkg.GeneEncodingJSON,
				Payload:  json.RawMessage(`[0.1,0.2]`),
			},
			ReproducibilityMetadata: resultpkg.ReproducibilityMetadata{
				SchemaVersion:      resultpkg.SchemaVersionV533,
				FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
				FingerprintVersion: resultpkg.FingerprintVersionV1,
			},
		},
		Promote: resultpkg.PromoteLayer{DecisionStatus: resultpkg.DecisionStatusPending},
		// Verification.OOSResult.Status left zero-value ("").
	}
	blob, err := json.Marshal(&pkg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return blob
}

func TestTransformOOSBlob_StampsNotRunOnEmptyStatus(t *testing.T) {
	rec := store.GeneRecord{
		ChallengerID:    "ch-old-001",
		FullPackageJSON: preFiveDBlob(t),
	}
	out, err := transformOOSBlob(rec)
	if err != nil {
		t.Fatalf("transformOOSBlob: %v", err)
	}
	if out == nil {
		t.Fatal("empty status must be rewritten, got nil (skip)")
	}
	var got resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Verification.OOSResult.Status != resultpkg.VerificationStatusNotRun {
		t.Errorf("Status = %q, want not_run", got.Verification.OOSResult.Status)
	}
	if got.Verification.OOSResult.Notes == nil {
		t.Error("Notes is nil; backfill must explain the stamp")
	} else if !strings.Contains(*got.Verification.OOSResult.Notes, "backfilled") {
		t.Errorf("Notes missing 'backfilled' marker: %q", *got.Verification.OOSResult.Notes)
	}
	// Other layers untouched — Promote should still be pending.
	if got.Promote.DecisionStatus != resultpkg.DecisionStatusPending {
		t.Errorf("Promote.DecisionStatus = %q, want pending (migration must touch only OOSResult)",
			got.Promote.DecisionStatus)
	}
	if got.Core.StrategyID != "sigmoid_v1" {
		t.Errorf("Core.StrategyID = %q, want sigmoid_v1", got.Core.StrategyID)
	}

	// Result must satisfy resultpkg.Validate so the round-trip is
	// front-end safe (the enum check was the entire motivation for
	// this migration).
	if err := got.Validate(); err != nil {
		t.Errorf("validate after backfill: %v", err)
	}
}

func TestTransformOOSBlob_IdempotentWhenStatusAlreadySet(t *testing.T) {
	cases := []struct {
		name   string
		status resultpkg.VerificationStatus
	}{
		{"already not_run", resultpkg.VerificationStatusNotRun},
		{"already ok", resultpkg.VerificationStatusOK},
		{"already insufficient_data", resultpkg.VerificationStatusInsufficientData},
		{"already failed", resultpkg.VerificationStatusFailed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pkg := resultpkg.ChallengerResultPackage{
				Core:    resultpkg.ResultCore{StrategyID: "sigmoid_v1"},
				Promote: resultpkg.PromoteLayer{DecisionStatus: resultpkg.DecisionStatusPending},
				Verification: resultpkg.VerificationLayer{
					OOSResult: resultpkg.OOSResult{Status: c.status},
				},
			}
			blob, _ := json.Marshal(&pkg)
			rec := store.GeneRecord{ChallengerID: "ch-already", FullPackageJSON: blob}
			out, err := transformOOSBlob(rec)
			if err != nil {
				t.Fatalf("transformOOSBlob: %v", err)
			}
			if out != nil {
				t.Errorf("status=%q is non-empty — expected skip, got %d bytes rewrite",
					c.status, len(out))
			}
		})
	}
}

func TestTransformOOSBlob_EmptyBlobIsNoOp(t *testing.T) {
	rec := store.GeneRecord{ChallengerID: "ch-no-blob"}
	out, err := transformOOSBlob(rec)
	if err != nil {
		t.Fatalf("empty blob: %v", err)
	}
	if out != nil {
		t.Errorf("nil blob in → nil out expected, got %d bytes", len(out))
	}
}

func TestTransformOOSBlob_RejectsMalformedJSON(t *testing.T) {
	rec := store.GeneRecord{
		ChallengerID:    "ch-bad-json",
		FullPackageJSON: []byte("not-json"),
	}
	_, err := transformOOSBlob(rec)
	if err == nil {
		t.Error("malformed JSON must surface as error, not silent skip")
	}
}

func TestTransformOOSBlob_PreservesExistingAlphaWhenStatusSet(t *testing.T) {
	// Defensive: if a row has ok status + alpha numbers (somehow
	// shipped before migration ran), the migration must NOT overwrite
	// them. Idempotency contract: non-empty status → skip.
	alpha := 0.07
	color := resultpkg.DecisionColorGreen
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{StrategyID: "sigmoid_v1"},
		Verification: resultpkg.VerificationLayer{
			OOSResult: resultpkg.OOSResult{
				Status:          resultpkg.VerificationStatusOK,
				OOSAlphaMonthly: &alpha,
				DecisionColor:   &color,
			},
		},
	}
	blob, _ := json.Marshal(&pkg)
	rec := store.GeneRecord{ChallengerID: "ch-ok", FullPackageJSON: blob}
	out, err := transformOOSBlob(rec)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("ok status with alpha numbers must be skipped, got rewrite (%d bytes)", len(out))
	}
}
