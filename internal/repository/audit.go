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

// IsAccountFrozen reports whether an account is currently kill-latched
// (B1 server-side persistent latch): true iff the most recent kill/resume
// audit event is a kill. This is the durable freeze state the WS handshake
// re-asserts to a (re)connecting Agent via auth_ok.Frozen, so a killed
// agent stays HALTED across restarts. (nil, never-killed) → not frozen.
//
// The kill/resume audit row IS the latch event — the same record that
// powers the /live banner — so reusing it keeps a single source of truth
// for "is this account frozen?" with no separate enforcement table.
func (r *AuditRepo) IsAccountFrozen(ctx context.Context, accountID string) (bool, error) {
	row, err := r.LatestKillOrResume(ctx, accountID)
	if err != nil {
		return false, err
	}
	return row != nil && row.Action == store.AuditActionInstanceKill, nil
}
