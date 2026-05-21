// KLineGapRepo is a read-only listing for the kline_gaps table. The
// table is populated by the datafeeder during import (Phase 1.5);
// nothing else writes to it. The HTTP layer exposes this listing via
// GET /api/v1/data/gaps so ops can audit data completeness without a
// DB shell.
package repository

import (
	"context"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// KLineGapRepo wraps the gorm.DB for kline_gaps lookups.
type KLineGapRepo struct {
	db *gorm.DB
}

// NewKLineGapRepo wraps a *gorm.DB.
func NewKLineGapRepo(db *gorm.DB) *KLineGapRepo {
	return &KLineGapRepo{db: db}
}

// List returns all gaps for (symbol, interval), ordered by GapStartMs
// ascending. limit caps the row count; 0 (or negative) returns
// everything matching — callers should pass a sane upper bound for
// production use (the table can grow large in long-running imports).
func (r *KLineGapRepo) List(ctx context.Context, symbol, interval string, limit int) ([]store.KLineGap, error) {
	q := r.db.WithContext(ctx).
		Where("symbol = ? AND interval = ?", symbol, interval).
		Order("gap_start_ms ASC")
	if limit > 0 {
		q = q.Limit(limit)
	}
	var rows []store.KLineGap
	if err := q.Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
