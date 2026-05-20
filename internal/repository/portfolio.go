// portfolio.go — PortfolioRepo reads / appends PortfolioState rows.
// Append-only history per CC1 + §5.1 (one row per Tick).
package repository

import (
	"context"
	"errors"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

type PortfolioRepo struct {
	db *gorm.DB
}

func NewPortfolioRepo(db *gorm.DB) *PortfolioRepo {
	return &PortfolioRepo{db: db}
}

// Latest returns the most recent PortfolioState for an instance.
// Returns (nil, nil) when no rows exist — caller treats as cold start.
// Any other error propagates (treat as transient infra failure).
func (r *PortfolioRepo) Latest(ctx context.Context, instanceID string) (*store.PortfolioState, error) {
	var ps store.PortfolioState
	err := r.db.WithContext(ctx).
		Where("instance_id = ?", instanceID).
		Order("now_ms DESC").
		Limit(1).
		First(&ps).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ps, nil
}

// Append inserts one new PortfolioState row. Composite PK
// (instance_id, now_ms) — duplicate NowMs is a programmer error
// (Tick is per-instance serial) and will surface as a unique violation.
func (r *PortfolioRepo) Append(ctx context.Context, ps *store.PortfolioState) error {
	if ps == nil {
		return errors.New("repository.PortfolioRepo.Append: nil state")
	}
	return r.db.WithContext(ctx).Create(ps).Error
}
