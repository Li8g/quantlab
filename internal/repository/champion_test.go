package repository

import (
	"testing"
	"time"

	"quantlab/internal/api"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

func okGeneRecord() store.GeneRecord {
	return store.GeneRecord{
		ChallengerID:   "ch-001",
		StrategyID:     "sigmoid_v1",
		Pair:           "BTCUSDT",
		TestMode:       false,
		DecisionStatus: resultpkg.DecisionStatusPending,
	}
}

func TestApplyPromote_HappyPath(t *testing.T) {
	rec := okGeneRecord()
	note := "approved by review committee"
	req := api.PromoteChallengerRequest{ReviewedBy: "alice", DecisionNote: &note}
	now := time.Unix(1_700_000_000, 0).UTC()

	updates, history, err := applyPromote(rec, req, now)
	if err != nil {
		t.Fatalf("applyPromote: %v", err)
	}
	if updates["decision_status"] != resultpkg.DecisionStatusPromoted {
		t.Errorf("decision_status update = %v, want promoted", updates["decision_status"])
	}
	if updates["reviewed_at_ts"] != now.UnixMilli() {
		t.Errorf("reviewed_at_ts = %v, want %d", updates["reviewed_at_ts"], now.UnixMilli())
	}
	if updates["reviewed_by"] != "alice" {
		t.Errorf("reviewed_by = %v, want alice", updates["reviewed_by"])
	}
	if updates["decision_note"] != note {
		t.Errorf("decision_note = %v, want %q", updates["decision_note"], note)
	}
	if history.ChallengerID != "ch-001" || history.StrategyID != "sigmoid_v1" || history.Pair != "BTCUSDT" {
		t.Errorf("history identity mismatch: %+v", history)
	}
	if !history.PromotedAt.Equal(now) {
		t.Errorf("PromotedAt = %v, want %v", history.PromotedAt, now)
	}
}

func TestApplyPromote_RejectsTestMode(t *testing.T) {
	rec := okGeneRecord()
	rec.TestMode = true
	req := api.PromoteChallengerRequest{ReviewedBy: "alice"}

	_, _, err := applyPromote(rec, req, time.Now().UTC())
	if err == nil {
		t.Error("TestMode=true must reject Promote")
	}
}

func TestApplyPromote_RejectsAlreadyPromoted(t *testing.T) {
	rec := okGeneRecord()
	rec.DecisionStatus = resultpkg.DecisionStatusPromoted
	req := api.PromoteChallengerRequest{ReviewedBy: "alice"}

	_, _, err := applyPromote(rec, req, time.Now().UTC())
	if err == nil {
		t.Error("already-promoted challenger must reject double-Promote")
	}
}

func TestApplyPromote_RejectsAlreadyRejected(t *testing.T) {
	rec := okGeneRecord()
	rec.DecisionStatus = resultpkg.DecisionStatusRejected
	req := api.PromoteChallengerRequest{ReviewedBy: "alice"}

	_, _, err := applyPromote(rec, req, time.Now().UTC())
	if err == nil {
		t.Error("rejected challenger must refuse Promote (DecisionStatus is terminal)")
	}
}

func TestApplyPromote_NilNoteSkipsField(t *testing.T) {
	rec := okGeneRecord()
	req := api.PromoteChallengerRequest{ReviewedBy: "alice", DecisionNote: nil}

	updates, _, err := applyPromote(rec, req, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := updates["decision_note"]; ok {
		t.Errorf("nil DecisionNote should not produce decision_note update, got %v", updates["decision_note"])
	}
}

func TestApplyRetire_HappyPath(t *testing.T) {
	history := store.ChampionHistory{
		ChallengerID: "ch-001",
		PromotedAt:   time.Unix(1_700_000_000, 0).UTC(),
	}
	note := "replaced by superior fitness"
	req := api.RetireChampionRequest{ReviewedBy: "bob", DecisionNote: &note}
	now := time.Unix(1_700_900_000, 0).UTC()

	updates, err := applyRetire(history, req, now)
	if err != nil {
		t.Fatalf("applyRetire: %v", err)
	}
	if updates["retired_at"] != now {
		t.Errorf("retired_at = %v, want %v", updates["retired_at"], now)
	}
	if updates["retired_by"] != "bob" {
		t.Errorf("retired_by = %v, want bob", updates["retired_by"])
	}
	if updates["retire_note"] != note {
		t.Errorf("retire_note = %v, want %q", updates["retire_note"], note)
	}
}

func TestApplyRetire_RejectsAlreadyRetired(t *testing.T) {
	already := time.Unix(1_700_500_000, 0).UTC()
	history := store.ChampionHistory{
		ChallengerID: "ch-001",
		PromotedAt:   time.Unix(1_700_000_000, 0).UTC(),
		RetiredAt:    &already,
	}
	req := api.RetireChampionRequest{ReviewedBy: "bob"}

	_, err := applyRetire(history, req, time.Now().UTC())
	if err == nil {
		t.Error("already-retired champion must reject Retire")
	}
}

func TestApplyRetire_NilNoteSkipsField(t *testing.T) {
	history := store.ChampionHistory{ChallengerID: "ch-001"}
	req := api.RetireChampionRequest{ReviewedBy: "bob", DecisionNote: nil}

	updates, err := applyRetire(history, req, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := updates["retire_note"]; ok {
		t.Errorf("nil DecisionNote should not produce retire_note update")
	}
}
