package migrate

import (
	"encoding/json"
	"testing"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// pendingBlob builds a minimal package whose PromoteLayer is the
// eval-time "pending" state — the shape every pre-fix promoted row
// has stored in full_package_json.
func pendingBlob(t *testing.T) []byte {
	t.Helper()
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{
			StrategyID:         "sigmoid_v1",
			SchemaVersion:      resultpkg.SchemaVersionV533,
			FitnessVersion:     resultpkg.FitnessVersionV1RawStd,
			FingerprintVersion: resultpkg.FingerprintVersionV1,
		},
		Promote: resultpkg.PromoteLayer{
			DecisionStatus: resultpkg.DecisionStatusPending,
		},
	}
	blob, err := json.Marshal(&pkg)
	if err != nil {
		t.Fatalf("marshal pendingBlob: %v", err)
	}
	return blob
}

func TestTransformPromoteBlob_RewritesStalePromoteLayer(t *testing.T) {
	reviewer := "alice"
	ts := int64(1_700_000_000_000)
	note := "approved by review committee"
	rec := store.GeneRecord{
		ChallengerID:    "ch-001",
		DecisionStatus:  resultpkg.DecisionStatusPromoted,
		ReviewedBy:      &reviewer,
		ReviewedAtTS:    &ts,
		DecisionNote:    &note,
		FullPackageJSON: pendingBlob(t),
	}
	out, err := transformPromoteBlob(rec)
	if err != nil {
		t.Fatalf("transformPromoteBlob: %v", err)
	}
	if out == nil {
		t.Fatal("expected blob to be rewritten, got nil (skip signal)")
	}

	var got resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Promote.DecisionStatus != resultpkg.DecisionStatusPromoted {
		t.Errorf("DecisionStatus = %q, want promoted", got.Promote.DecisionStatus)
	}
	if got.Promote.ReviewedBy == nil || *got.Promote.ReviewedBy != "alice" {
		t.Errorf("ReviewedBy = %v, want alice", got.Promote.ReviewedBy)
	}
	if got.Promote.ReviewedAtTS == nil || *got.Promote.ReviewedAtTS != ts {
		t.Errorf("ReviewedAtTS = %v, want %d", got.Promote.ReviewedAtTS, ts)
	}
	if got.Promote.DecisionNote == nil || *got.Promote.DecisionNote != note {
		t.Errorf("DecisionNote = %v, want %q", got.Promote.DecisionNote, note)
	}
	// Non-Promote layers must be untouched. A regression here would
	// corrupt audit metadata (Core.StrategyID, schema versions).
	if got.Core.StrategyID != "sigmoid_v1" {
		t.Errorf("Core.StrategyID = %q, want sigmoid_v1 (migration must not touch Core)", got.Core.StrategyID)
	}
}

func TestTransformPromoteBlob_IdempotentWhenAlreadyInSync(t *testing.T) {
	// Blob already reflects the promoted state — backfill must skip.
	pkg := resultpkg.ChallengerResultPackage{
		Core: resultpkg.ResultCore{StrategyID: "sigmoid_v1"},
		Promote: resultpkg.PromoteLayer{
			DecisionStatus: resultpkg.DecisionStatusPromoted,
		},
	}
	blob, err := json.Marshal(&pkg)
	if err != nil {
		t.Fatal(err)
	}
	rec := store.GeneRecord{
		ChallengerID:    "ch-002",
		DecisionStatus:  resultpkg.DecisionStatusPromoted,
		FullPackageJSON: blob,
	}
	out, err := transformPromoteBlob(rec)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("blob already in sync — expected nil to skip, got %d bytes", len(out))
	}
}

func TestTransformPromoteBlob_EmptyBlobIsNoOp(t *testing.T) {
	// Historical rows occasionally lack a blob (engine bug, manual
	// fixture). Treat as "nothing to migrate" rather than erroring,
	// so a single bad row doesn't halt the batch.
	rec := store.GeneRecord{
		ChallengerID:   "ch-003",
		DecisionStatus: resultpkg.DecisionStatusPromoted,
	}
	out, err := transformPromoteBlob(rec)
	if err != nil {
		t.Fatalf("empty blob: %v", err)
	}
	if out != nil {
		t.Errorf("empty blob in → nil out expected, got %d bytes", len(out))
	}
}

func TestTransformPromoteBlob_RejectsMalformedJSON(t *testing.T) {
	rec := store.GeneRecord{
		ChallengerID:    "ch-004",
		DecisionStatus:  resultpkg.DecisionStatusPromoted,
		FullPackageJSON: []byte("not-json"),
	}
	_, err := transformPromoteBlob(rec)
	if err == nil {
		t.Error("malformed JSON must return error, not silently skip")
	}
}

func TestTransformPromoteBlob_RejectedStateBackfilled(t *testing.T) {
	// Promote API doesn't write rejected today, but the migration
	// is the canonical column → blob sync, so it must handle the
	// hypothetical rejected case without dropping reviewer info.
	reviewer := "bob"
	ts := int64(1_700_500_000_000)
	rec := store.GeneRecord{
		ChallengerID:    "ch-005",
		DecisionStatus:  resultpkg.DecisionStatusRejected,
		ReviewedBy:      &reviewer,
		ReviewedAtTS:    &ts,
		FullPackageJSON: pendingBlob(t),
	}
	out, err := transformPromoteBlob(rec)
	if err != nil {
		t.Fatalf("transformPromoteBlob: %v", err)
	}
	if out == nil {
		t.Fatal("rejected backfill must rewrite blob")
	}
	var got resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatal(err)
	}
	if got.Promote.DecisionStatus != resultpkg.DecisionStatusRejected {
		t.Errorf("DecisionStatus = %q, want rejected", got.Promote.DecisionStatus)
	}
	if got.Promote.ReviewedBy == nil || *got.Promote.ReviewedBy != "bob" {
		t.Errorf("ReviewedBy mismatch")
	}
}
