// K-line loader: reads store.KLine rows for a (symbol, interval, time
// range) tuple and returns the matching []domain.Bar series for use by
// BuildEvaluablePlan. Source-of-truth: docs/Coding-plan §5D + §M16.
//
// Gap markup deferred: Bar.IsGap / Bar.GapType are excluded from
// bars_hash by spec (internal/quant/canonical_json.go), so a loader
// that returns IsGap=false uniformly does not affect reproducibility
// metadata. A future gap-aware loader can layer on top by consulting
// store.KLineGap rows; that's Phase 5C+ data-infra work, not blocking
// for 5D-epoch.
package data

import (
	"context"
	"errors"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/domain"
	"quantlab/internal/saas/store"
)

// LoadKLines fetches all KLine rows for the given (symbol, interval)
// whose OpenTime falls in [startMs, endMs] inclusive, ordered ascending
// by OpenTime. Returns an empty slice when no rows match (the caller
// decides whether that's an error).
//
// The interval string ("1m", "1h", ...) must match the value the
// datafeeder used when inserting rows; KLine has a composite primary
// key over (Symbol, Interval, OpenTime).
func LoadKLines(
	ctx context.Context,
	db *gorm.DB,
	symbol, interval string,
	startMs, endMs int64,
) ([]domain.Bar, error) {
	if db == nil {
		return nil, errors.New("data.LoadKLines: nil db")
	}
	if symbol == "" || interval == "" {
		return nil, errors.New("data.LoadKLines: empty symbol or interval")
	}
	if endMs < startMs {
		return nil, fmt.Errorf("data.LoadKLines: endMs=%d < startMs=%d", endMs, startMs)
	}

	var rows []store.KLine
	if err := db.WithContext(ctx).
		Where("symbol = ? AND interval = ? AND open_time BETWEEN ? AND ?",
			symbol, interval, startMs, endMs).
		Order("open_time ASC").
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("data.LoadKLines: %w", err)
	}

	bars := make([]domain.Bar, len(rows))
	for i, r := range rows {
		bars[i] = domain.Bar{
			OpenTime: r.OpenTime,
			Open:     r.Open,
			High:     r.High,
			Low:      r.Low,
			Close:    r.Close,
			Volume:   r.Volume,
		}
	}
	return bars, nil
}

// IntervalToMs maps the canonical interval whitelist to milliseconds.
// Mirrors the api/validate allowedIntervals set; returns 0 + error for
// any other value so the SaaS layer fails loudly on an unconfigured
// bar interval rather than silently picking a wrong barIntervalMs for
// strategy initialisation.
func IntervalToMs(interval string) (int64, error) {
	switch interval {
	case "1m":
		return 60_000, nil
	case "5m":
		return 5 * 60_000, nil
	case "15m":
		return 15 * 60_000, nil
	case "1h":
		return 60 * 60_000, nil
	case "4h":
		return 4 * 60 * 60_000, nil
	case "1d":
		return 24 * 60 * 60_000, nil
	default:
		return 0, fmt.Errorf("data.IntervalToMs: unsupported interval %q", interval)
	}
}
