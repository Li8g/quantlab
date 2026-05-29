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

// ListDiscrepanciesForInstance returns the most-recent drift rows for an
// instance, newest first. Used by the /live snapshot's reconciliation
// tail (Tier L). An empty instanceID returns (nil, nil) without a query.
func (r *ReconRepo) ListDiscrepanciesForInstance(ctx context.Context, instanceID string, limit int) ([]store.ReconciliationDiscrepancy, error) {
	if instanceID == "" {
		return nil, nil
	}
	var rows []store.ReconciliationDiscrepancy
	err := r.db.WithContext(ctx).
		Where("instance_id = ?", instanceID).
		Order("detected_at_ms DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// ListAgentErrorsForInstance returns the most-recent agent errors for an
// instance, newest first. Used by the /live snapshot's error stream.
func (r *ReconRepo) ListAgentErrorsForInstance(ctx context.Context, instanceID string, limit int) ([]store.AgentError, error) {
	if instanceID == "" {
		return nil, nil
	}
	var rows []store.AgentError
	err := r.db.WithContext(ctx).
		Where("instance_id = ?", instanceID).
		Order("occurred_at_ms DESC").
		Limit(limit).
		Find(&rows).Error
	if err != nil {
		return nil, err
	}
	return rows, nil
}
