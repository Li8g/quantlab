// bar_loader.go — default BarLoader backed by data.LoadKLines. Phase 6
// Tick consumes 1m bars only (per docs/系统总体拓扑结构.md §6.2 step b).
package instance

import (
	"context"
	"fmt"

	"gorm.io/gorm"

	"quantlab/internal/data"
	"quantlab/internal/domain"
)

// DefaultBarLoader wraps a *gorm.DB; pulls trailing 1m bars from the
// klines hypertable.
type DefaultBarLoader struct {
	DB *gorm.DB
}

// LoadTrailing returns the `count` most recent 1m bars at or before
// nowMs, sorted ascending. If the DB has fewer than `count` bars in
// the window, returns what's available (may be empty).
//
// Window: [nowMs - count*1m, nowMs] inclusive. Caller (Tick) treats an
// empty or too-stale newest bar as "no fresh data" and skips the cycle
// before Step (see Manager.effMaxBarStalenessMs / ErrInstanceDataStale),
// so the strategy never decides against a stale or zero close.
func (l *DefaultBarLoader) LoadTrailing(ctx context.Context, pair string, count int, nowMs int64) ([]domain.Bar, error) {
	if count <= 0 {
		return nil, fmt.Errorf("bar loader: count must be > 0, got %d", count)
	}
	startMs := nowMs - int64(count)*barIntervalMs
	bars, err := data.LoadKLines(ctx, l.DB, pair, "1m", startMs, nowMs)
	if err != nil {
		return nil, fmt.Errorf("bar loader: load klines: %w", err)
	}
	// data.LoadKLines returns ascending by OpenTime already; trim
	// excess from the head if the underlying table holds more than
	// count.
	if len(bars) > count {
		bars = bars[len(bars)-count:]
	}
	return bars, nil
}
