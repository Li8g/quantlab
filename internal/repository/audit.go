package repository

import (
	"context"

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
