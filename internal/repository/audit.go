package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// AuditRepo writes the insert-only AuditLog action trail (§7.1). The table
// has been schema-ready since Tier 2 but had no producer until the
// kill_switch action trail (Option 3 step 5) needed one — this is the
// first writer.
type AuditRepo struct{ db *gorm.DB }

func NewAuditRepo(db *gorm.DB) *AuditRepo { return &AuditRepo{db: db} }

// Insert appends one audit event. CreatedAt is filled by gorm's
// autoCreateTime. Insert-only: rows are never updated or deleted.
func (r *AuditRepo) Insert(ctx context.Context, e *store.AuditLog) error {
	return r.db.WithContext(ctx).Create(e).Error
}

// LatestKillOrResume returns the most recent instance.kill OR
// instance.resume audit event for an account (subject "account:<id>"), or
// (nil, nil) when neither ever happened. Powers the /live frozen banner
// (Option 3 step 4): the caller shows the banner only when the latest
// event is a kill, so a resume (§5.13 v2) clears it.
func (r *AuditRepo) LatestKillOrResume(ctx context.Context, accountID string) (*store.AuditLog, error) {
	var row store.AuditLog
	err := r.db.WithContext(ctx).
		Where("action IN ? AND subject = ?",
			[]store.AuditAction{store.AuditActionInstanceKill, store.AuditActionInstanceResume},
			"account:"+accountID).
		Order("created_at DESC").
		First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &row, nil
}
