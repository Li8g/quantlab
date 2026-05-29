// reconciliation.go — ReconRepo appends the two Phase 8 forensic tables
// fed by delta_report (Option 2): ReconciliationDiscrepancy (position
// drift vs SaaS bookkeeping) and AgentError (exchange-layer errors the
// Agent collected). Both are append-only audit trails — durable and
// queryable so incident retrospective survives log rotation.
package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

type ReconRepo struct {
	db *gorm.DB
}

func NewReconRepo(db *gorm.DB) *ReconRepo {
	return &ReconRepo{db: db}
}

// InsertDiscrepancy appends one detected position drift.
func (r *ReconRepo) InsertDiscrepancy(ctx context.Context, d *store.ReconciliationDiscrepancy) error {
	if d == nil {
		return errors.New("repository.ReconRepo.InsertDiscrepancy: nil discrepancy")
	}
	return r.db.WithContext(ctx).Create(d).Error
}

// InsertAgentError appends one exchange-layer error from a delta_report.
func (r *ReconRepo) InsertAgentError(ctx context.Context, e *store.AgentError) error {
	if e == nil {
		return errors.New("repository.ReconRepo.InsertAgentError: nil error")
	}
	return r.db.WithContext(ctx).Create(e).Error
}
