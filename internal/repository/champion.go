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
// Both operations are wrapped in transactions, but the transaction alone
// does NOT serialize concurrent promotes: under READ COMMITTED two promotes
// for the same (strategy_id, pair) can both read activeOther=0. The DB-level
// uq_champion_active partial unique index (store/db.go) is what makes the
// loser fail loudly (mapped to ErrActiveChampionExists) instead of silently
// creating a second active champion.
package repository

import (
	"context"
	"encoding/json"
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
		// Count other-challenger active rows for this (strategy, pair).
		// "<>" filter is defensive — applyPromote also rejects self via
		// DecisionStatusPromoted, so a self-collision can't reach here.
		var activeOther int64
		if err := tx.Model(&store.ChampionHistory{}).
			Where("strategy_id = ? AND pair = ? AND retired_at IS NULL AND challenger_id <> ?",
				rec.StrategyID, rec.Pair, rec.ChallengerID).
			Count(&activeOther).Error; err != nil {
			return fmt.Errorf("count active champion_history: %w", err)
		}
		now := time.Now().UTC()
		updates, history, err := applyPromote(rec, req, now, int(activeOther))
		if err != nil {
			return err
		}
		// Rewrite the JSON blob's PromoteLayer so /package GETs reflect
		// the post-promote state, not the eval-time pending snapshot.
		// Same `now` as applyPromote so column + blob timestamps match.
		blob, err := applyPromoteToBlob(rec.FullPackageJSON, req, now)
		if err != nil {
			return err
		}
		if blob != nil {
			updates["full_package_json"] = blob
		}
		if err := tx.Model(&store.GeneRecord{}).
			Where("challenger_id = ?", challengerID).
			Updates(updates).Error; err != nil {
			return fmt.Errorf("update gene_record: %w", err)
		}
		if err := tx.Create(&history).Error; err != nil {
			// The activeOther count above is a READ COMMITTED snapshot, so a
			// concurrent Promote for the same (strategy_id, pair) can slip
			// past it. The uq_champion_active partial unique index is the
			// real backstop: the loser's INSERT trips it here.
			if isUniqueViolation(err) {
				return api.ErrActiveChampionExists
			}
			return fmt.Errorf("create champion_history: %w", err)
		}
		return nil
	})
}

// applyPromoteToBlob re-marshals the challenger's full_package_json with
// the post-promote PromoteLayer (DecisionStatus=promoted + reviewer +
// timestamps). Returns nil when the input blob is empty so the caller
// leaves the column untouched. Pure function: no DB, no I/O.
func applyPromoteToBlob(blob []byte, req api.PromoteChallengerRequest, now time.Time) ([]byte, error) {
	if len(blob) == 0 {
		return nil, nil
	}
	var pkg resultpkg.ChallengerResultPackage
	if err := json.Unmarshal(blob, &pkg); err != nil {
		return nil, fmt.Errorf("unmarshal full_package_json: %w", err)
	}
	ts := now.UnixMilli()
	rb := req.ReviewedBy
	pkg.Promote.DecisionStatus = resultpkg.DecisionStatusPromoted
	pkg.Promote.ReviewedAtTS = &ts
	pkg.Promote.ReviewedBy = &rb
	if req.DecisionNote != nil {
		pkg.Promote.DecisionNote = req.DecisionNote
	}
	return json.Marshal(&pkg)
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
		return retireHistory(tx, history, req, time.Now().UTC())
	})
}

func retireHistory(tx *gorm.DB, history store.ChampionHistory, req api.RetireChampionRequest, now time.Time) error {
	updates, err := applyRetire(history, req, now)
	if err != nil {
		return err
	}
	res := tx.Model(&store.ChampionHistory{}).
		Where("id = ? AND retired_at IS NULL", history.ID).
		Updates(updates)
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return api.ErrAlreadyRetired
	}
	return nil
}

// List returns ChampionHistory rows ordered by PromotedAt descending
// (most-recently promoted first). Both strategyID and pair are
// optional filters — empty string means "no filter on that field".
// limit ≤ 0 returns all rows; callers should cap externally.
func (r *ChampionRepo) List(ctx context.Context, strategyID, pair string, limit int) ([]store.ChampionHistory, error) {
	q := r.db.WithContext(ctx).Order("promoted_at DESC")
	if strategyID != "" {
		q = q.Where("strategy_id = ?", strategyID)
	}
	if pair != "" {
		q = q.Where("pair = ?", pair)
	}
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []store.ChampionHistory
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// GetActive returns the unretired ChampionHistory row for the given
// (strategy_id, pair) pair. "Active" = RetiredAt IS NULL. Returns
// gorm.ErrRecordNotFound when no active champion exists; callers map
// that to HTTP 404.
//
// Invariant: at most one row per (strategy_id, pair) has
// RetiredAt IS NULL at any time, enforced by Promote refusing to
// double-promote without an intervening Retire. This method does
// NOT defend against schema corruption — if two active rows exist,
// it returns the most-recently-promoted one and lets a future
// integrity check surface the anomaly.
func (r *ChampionRepo) GetActive(ctx context.Context, strategyID, pair string) (*store.ChampionHistory, error) {
	if strategyID == "" || pair == "" {
		return nil, errors.New("repository.ChampionRepo.GetActive: strategyID and pair required")
	}
	var row store.ChampionHistory
	if err := r.db.WithContext(ctx).
		Where("strategy_id = ? AND pair = ? AND retired_at IS NULL", strategyID, pair).
		Order("promoted_at DESC").
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// GetByChallengerID returns the champion_history row tied to this
// challenger, regardless of retirement state. Promote creates exactly
// one row per challenger, so the lookup is unambiguous. Returns
// gorm.ErrRecordNotFound when the challenger was never promoted —
// callers treat that as "not a champion" rather than a hard error.
func (r *ChampionRepo) GetByChallengerID(ctx context.Context, challengerID string) (*store.ChampionHistory, error) {
	if challengerID == "" {
		return nil, errors.New("repository.ChampionRepo.GetByChallengerID: empty challengerID")
	}
	var row store.ChampionHistory
	if err := r.db.WithContext(ctx).
		Where("challenger_id = ?", challengerID).
		First(&row).Error; err != nil {
		return nil, err
	}
	return &row, nil
}

// applyPromote is the pure decision kernel for Promote. Returns the
// field-update map for gene_records and the new champion_history row.
// All preconditions are checked here so a unit test can drive every
// reject path without a DB.
//
// activeOtherCount is the number of champion_history rows for the same
// (strategy_id, pair) that are still live (RetiredAt IS NULL) and
// belong to a different challenger. The caller queries this from the
// DB before calling — passing it in keeps the kernel pure-testable.
func applyPromote(
	rec store.GeneRecord,
	req api.PromoteChallengerRequest,
	now time.Time,
	activeOtherCount int,
) (map[string]interface{}, store.ChampionHistory, error) {
	if rec.TestMode {
		return nil, store.ChampionHistory{}, api.ErrCannotPromoteTestMode
	}
	if rec.DecisionStatus == resultpkg.DecisionStatusPromoted {
		return nil, store.ChampionHistory{}, api.ErrAlreadyPromoted
	}
	if rec.DecisionStatus == resultpkg.DecisionStatusRejected {
		return nil, store.ChampionHistory{}, api.ErrAlreadyRejected
	}
	if activeOtherCount > 0 {
		return nil, store.ChampionHistory{}, api.ErrActiveChampionExists
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
		return nil, api.ErrAlreadyRetired
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
