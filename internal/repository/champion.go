// Champion lifecycle persistence: Promote (challenger → champion) and
// Retire (champion → retired). Source-of-truth: CLAUDE.md
// "Champion retirement is managed in champion_history table via a
// separate API" + docs/Coding-plan §M15.
//
// Two invariants enforced here:
//   - TestMode=true challengers MUST NOT be Promoted (the framework
//     considers their friction zero and thus unrepresentative of live
//     trading economics).
//   - DecisionStatus is the source of truth for the challenger's
//     promote/reject state; "retired" never appears on GeneRecord —
//     it lives on ChampionHistory.RetiredAt.
//
// Both operations are wrapped in transactions so concurrent races
// (double-Promote, Retire-while-Promoting) fail loudly rather than
// silently corrupt state.
package repository

import (
	"context"
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/api"
	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/store"
)

// ChampionRepo persists Promote + Retire transitions.
type ChampionRepo struct {
	db *gorm.DB
}

// NewChampionRepo wraps a *gorm.DB. The champion_history + gene_records
// tables are expected to exist (store.NewDB AutoMigrate provisions them).
func NewChampionRepo(db *gorm.DB) *ChampionRepo {
	return &ChampionRepo{db: db}
}

// Promote transitions a Pending challenger to Promoted and inserts a
// ChampionHistory row. Both operations run inside a transaction; a
// failure on either side rolls back. Returns gorm.ErrRecordNotFound
// when the challenger doesn't exist.
func (r *ChampionRepo) Promote(ctx context.Context, challengerID string, req api.PromoteChallengerRequest) error {
	if challengerID == "" {
		return errors.New("repository.ChampionRepo.Promote: empty challengerID")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var rec store.GeneRecord
		if err := tx.Where("challenger_id = ?", challengerID).First(&rec).Error; err != nil {
			return err
		}
		updates, history, err := applyPromote(rec, req, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := tx.Model(&store.GeneRecord{}).
			Where("challenger_id = ?", challengerID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update gene_record: %w", err)
		}
		if err := tx.Create(&history).Error; err != nil {
			return fmt.Errorf("create champion_history: %w", err)
		}
		return nil
	})
}

// Retire stamps RetiredAt + RetiredBy on the live ChampionHistory row
// for the given challengerID. "Live" = no RetiredAt set (one Promote
// can produce only one ChampionHistory entry; that's the row Retire
// updates). Returns gorm.ErrRecordNotFound for non-existent champions
// and a wrapped error for already-retired ones.
func (r *ChampionRepo) Retire(ctx context.Context, challengerID string, req api.RetireChampionRequest) error {
	if challengerID == "" {
		return errors.New("repository.ChampionRepo.Retire: empty challengerID")
	}
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var history store.ChampionHistory
		if err := tx.Where("challenger_id = ?", challengerID).First(&history).Error; err != nil {
			return err
		}
		updates, err := applyRetire(history, req, time.Now().UTC())
		if err != nil {
			return err
		}
		res := tx.Model(&store.ChampionHistory{}).
			Where("id = ?", history.ID).
			Updates(updates)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected == 0 {
			return fmt.Errorf("repository.ChampionRepo.Retire: update affected 0 rows (race?)")
		}
		return nil
	})
}

// applyPromote is the pure decision kernel for Promote. Returns the
// field-update map for gene_records and the new champion_history row.
// All preconditions are checked here so a unit test can drive every
// reject path without a DB.
func applyPromote(
	rec store.GeneRecord,
	req api.PromoteChallengerRequest,
	now time.Time,
) (map[string]interface{}, store.ChampionHistory, error) {
	if rec.TestMode {
		return nil, store.ChampionHistory{}, errors.New("cannot promote a TestMode=true challenger")
	}
	if rec.DecisionStatus == resultpkg.DecisionStatusPromoted {
		return nil, store.ChampionHistory{}, errors.New("challenger already promoted")
	}
	if rec.DecisionStatus == resultpkg.DecisionStatusRejected {
		return nil, store.ChampionHistory{}, errors.New("challenger already rejected; cannot promote")
	}
	ts := now.UnixMilli()
	updates := map[string]interface{}{
		"decision_status": resultpkg.DecisionStatusPromoted,
		"reviewed_at_ts":  ts,
		"reviewed_by":     req.ReviewedBy,
	}
	if req.DecisionNote != nil {
		updates["decision_note"] = *req.DecisionNote
	}
	history := store.ChampionHistory{
		StrategyID:   rec.StrategyID,
		Pair:         rec.Pair,
		ChallengerID: rec.ChallengerID,
		PromotedAt:   now,
	}
	return updates, history, nil
}

// applyRetire is the pure decision kernel for Retire. Returns the
// field-update map for champion_history. Refuses to double-Retire.
func applyRetire(
	history store.ChampionHistory,
	req api.RetireChampionRequest,
	now time.Time,
) (map[string]interface{}, error) {
	if history.RetiredAt != nil {
		return nil, errors.New("champion already retired")
	}
	reviewedBy := req.ReviewedBy
	updates := map[string]interface{}{
		"retired_at": now,
		"retired_by": reviewedBy,
	}
	if req.DecisionNote != nil {
		updates["retire_note"] = *req.DecisionNote
	}
	return updates, nil
}
