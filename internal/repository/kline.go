// KLineRepo is a read-only aggregate over the klines hypertable. The
// table is populated by the data orchestrator (Phase 1.5); this repo
// only reads. The HTTP layer exposes Coverage via GET
// /api/v1/data/coverage so ops and the frontend can see what data is
// loaded without a DB shell.
package repository

import (
	"context"

	"gorm.io/gorm"

	"quantlab/internal/saas/store"
)

// KLineRepo wraps the gorm.DB for klines aggregates.
type KLineRepo struct {
	db *gorm.DB
}

// NewKLineRepo wraps a *gorm.DB.
func NewKLineRepo(db *gorm.DB) *KLineRepo {
	return &KLineRepo{db: db}
}

// CoverageRow is one (symbol, interval) coverage summary returned by
// Coverage. Mirrors api.DataCoverageRow; the cmd/saas adapter bridges
// the two so repository need not import api.
type CoverageRow struct {
	Symbol    string `gorm:"column:symbol"`
	Interval  string `gorm:"column:interval"`
	MinOpenMs int64  `gorm:"column:min_open_ms"`
	MaxOpenMs int64  `gorm:"column:max_open_ms"`
	BarCount  int64  `gorm:"column:bar_count"`
}

// Coverage returns the stored-bar span (min/max open_time, ms) and bar
// count per (symbol, interval), ordered by (symbol, interval). With
// both args non-empty it filters to that one pair; otherwise it
// returns every pair. (The HTTP layer 400s the "exactly one arg" case
// before calling here — Coverage only filters when both are present.)
//
// [perf] The list-all path is a full-table GROUP BY — not
// time-prunable on the hypertable. Fine at prototype scale; before a
// full multi-year backfill, back it with a data_catalog summary table
// or a continuous aggregate refreshed on import.
func (r *KLineRepo) Coverage(ctx context.Context, symbol, interval string) ([]CoverageRow, error) {
	q := r.db.WithContext(ctx).
		Model(&store.KLine{}).
		Select("symbol, interval, min(open_time) AS min_open_ms, max(open_time) AS max_open_ms, count(*) AS bar_count").
		Group("symbol, interval").
		Order("symbol ASC, interval ASC")
	if symbol != "" && interval != "" {
		q = q.Where("symbol = ? AND interval = ?", symbol, interval)
	}
	var rows []CoverageRow
	if err := q.Scan(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
