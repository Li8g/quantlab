// orchestrator.go: drive monthly archive downloads → checksum verify →
// CSV parse → batched upsert into the klines hypertable, then scan the
// imported range for gaps and persist KLineGap rows.
//
// Scope (this iteration):
//   - Monthly archive ingest only. The API fallback hook (Orchestrator.API)
//     is present and tested by Phase 1.5-b but NOT wired into ImportSymbol
//     yet — the "near 1-2 days unarchived" path from phase plan §四 step 4
//     is a follow-up. Searches: "TODO: api fallback".
//   - Bulk insert uses GORM CreateInBatches with ON CONFLICT DO NOTHING.
//     Adequate for prototype-scale ranges (a few months at 1m); the full
//     9-year backfill (~4.7M rows) would want pgx.CopyFrom for the 50-100×
//     throughput gain phase plan §四 recommends. Marked [INVENTED v1].
//   - Idempotent: re-running ImportSymbol on an already-covered range is a
//     no-op for inserts (PK conflict → skip) and a refresh-in-place for
//     gap rows (existing gap rows in range are deleted and rewritten).
package data

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"quantlab/internal/saas/store"
)

// DefaultBatchSize is the GORM CreateInBatches batch size. Picked to fit
// comfortably under PG's max_locks_per_transaction in a single insert and
// to keep memory bounded.
const DefaultBatchSize = 1000

// ImportSummary is the return value of ImportSymbol — useful for the CLI
// and for end-to-end assertions in integration tests.
type ImportSummary struct {
	Symbol        string
	Interval      string
	StartMs       int64
	EndMs         int64
	MonthsFetched int
	RowsInserted  int64 // post-dedupe (PK conflicts excluded)
	GapsDetected  int
}

// Orchestrator owns one DB handle plus an archive + API client.
type Orchestrator struct {
	DB        *gorm.DB
	Archive   *ArchiveClient
	API       *APIClient // [TODO: api fallback] currently unused
	Source    string     // KLine.Source tag, default "binance.vision"
	BatchSize int
	Logger    *slog.Logger
}

func NewOrchestrator(db *gorm.DB) *Orchestrator {
	return &Orchestrator{
		DB:        db,
		Archive:   NewArchiveClient(),
		API:       NewAPIClient(),
		Source:    "binance.vision",
		BatchSize: DefaultBatchSize,
		Logger:    slog.Default(),
	}
}

// ImportSymbol fetches all klines in [start, end] for (symbol, interval),
// upserts them into the klines hypertable, and (re)detects gaps in that
// range. Returns a summary regardless of partial failure — callers should
// check both summary and err.
func (o *Orchestrator) ImportSymbol(
	ctx context.Context,
	symbol, interval string,
	start, end time.Time,
) (*ImportSummary, error) {
	if symbol == "" || interval == "" {
		return nil, fmt.Errorf("orchestrator: symbol and interval required")
	}
	if start.After(end) {
		return nil, fmt.Errorf("orchestrator: start %v after end %v", start, end)
	}

	startMs, endMs := start.UnixMilli(), end.UnixMilli()
	summary := &ImportSummary{
		Symbol: symbol, Interval: interval, StartMs: startMs, EndMs: endMs,
	}

	for _, m := range monthRange(start, end) {
		rows, err := o.fetchMonth(ctx, symbol, interval, m.Year, m.Month)
		if err != nil {
			// TODO: api fallback — on 404 for unarchived months, walk daily
			// archives or APIClient.FetchKlines for that month's days.
			return summary, fmt.Errorf("month %04d-%02d: %w", m.Year, m.Month, err)
		}
		summary.MonthsFetched++

		filtered := filterByOpenTime(rows, startMs, endMs)
		if len(filtered) == 0 {
			continue
		}
		inserted, err := o.bulkInsert(ctx, symbol, interval, filtered)
		if err != nil {
			return summary, fmt.Errorf("insert month %04d-%02d: %w", m.Year, m.Month, err)
		}
		summary.RowsInserted += inserted

		o.Logger.Info("orchestrator: ingested month",
			"symbol", symbol, "interval", interval,
			"year", m.Year, "month", m.Month,
			"rows_in_archive", len(rows), "rows_inserted", inserted)
	}

	gaps, err := o.detectAndPersistGaps(ctx, symbol, interval, startMs, endMs)
	if err != nil {
		return summary, fmt.Errorf("detect gaps: %w", err)
	}
	summary.GapsDetected = gaps
	return summary, nil
}

// fetchMonth downloads one month's archive + verifies its checksum +
// parses the CSV. Returns the rows in archive order.
func (o *Orchestrator) fetchMonth(
	ctx context.Context, symbol, interval string, year, month int,
) ([]KlineRow, error) {
	zipBody, err := o.Archive.DownloadMonthly(ctx, symbol, interval, year, month)
	if err != nil {
		return nil, fmt.Errorf("download zip: %w", err)
	}
	archiveURL := MonthlyKlineURL(o.Archive.BaseURL, symbol, interval, year, month)
	expected, err := o.Archive.DownloadChecksum(ctx, archiveURL)
	if err != nil {
		return nil, fmt.Errorf("download checksum: %w", err)
	}
	if err := VerifyChecksum(zipBody, expected); err != nil {
		return nil, fmt.Errorf("checksum: %w", err)
	}
	rows, err := ParseKlineCSV(zipBody)
	if err != nil {
		return nil, fmt.Errorf("parse csv: %w", err)
	}
	return rows, nil
}

// bulkInsert upserts rows into klines, returning the count of newly-
// inserted rows (PK conflicts excluded by ON CONFLICT DO NOTHING).
func (o *Orchestrator) bulkInsert(
	ctx context.Context, symbol, interval string, rows []KlineRow,
) (int64, error) {
	if len(rows) == 0 {
		return 0, nil
	}
	models := make([]store.KLine, len(rows))
	for i, r := range rows {
		models[i] = store.KLine{
			Symbol:      symbol,
			Interval:    interval,
			OpenTime:    r.OpenTime,
			Open:        r.Open,
			High:        r.High,
			Low:         r.Low,
			Close:       r.Close,
			Volume:      r.Volume,
			QuoteVolume: r.QuoteVolume,
			NumTrades:   r.NumTrades,
			Source:      o.Source,
		}
	}
	res := o.DB.WithContext(ctx).
		Clauses(clause.OnConflict{DoNothing: true}).
		CreateInBatches(&models, o.BatchSize)
	if res.Error != nil {
		return 0, res.Error
	}
	return res.RowsAffected, nil
}

// detectAndPersistGaps scans the klines table for missing bars in
// [startMs, endMs] and rewrites the corresponding KLineGap rows.
// Strategy: read all OpenTimes in range (sorted ASC); whenever the
// delta between consecutive bars exceeds IntervalMs(interval), record
// the missing span as a gap.
//
// Existing gap rows whose [start, end] falls within the scan range are
// deleted first to keep this idempotent — a re-run after backfilling
// previously-missing bars will correctly REMOVE the obsolete gap.
func (o *Orchestrator) detectAndPersistGaps(
	ctx context.Context, symbol, interval string, startMs, endMs int64,
) (int, error) {
	intervalMs, err := IntervalMs(interval)
	if err != nil {
		return 0, err
	}

	var openTimes []int64
	if err := o.DB.WithContext(ctx).Model(&store.KLine{}).
		Where("symbol = ? AND interval = ? AND open_time BETWEEN ? AND ?",
			symbol, interval, startMs, endMs).
		Order("open_time ASC").
		Pluck("open_time", &openTimes).Error; err != nil {
		return 0, fmt.Errorf("pluck open_times: %w", err)
	}

	gaps := computeGaps(openTimes, intervalMs, symbol, interval)

	if err := o.DB.WithContext(ctx).
		Where("symbol = ? AND interval = ? AND gap_start_ms >= ? AND gap_end_ms <= ?",
			symbol, interval, startMs, endMs).
		Delete(&store.KLineGap{}).Error; err != nil {
		return 0, fmt.Errorf("delete stale gaps: %w", err)
	}
	if len(gaps) > 0 {
		if err := o.DB.WithContext(ctx).Create(&gaps).Error; err != nil {
			return 0, fmt.Errorf("insert gaps: %w", err)
		}
	}
	return len(gaps), nil
}

// computeGaps is the pure-logic core of gap detection — extracted so it
// can be unit-tested without a database.
func computeGaps(openTimes []int64, intervalMs int64, symbol, interval string) []store.KLineGap {
	now := time.Now().UTC()
	var out []store.KLineGap
	for i := 1; i < len(openTimes); i++ {
		delta := openTimes[i] - openTimes[i-1]
		if delta > intervalMs {
			out = append(out, store.KLineGap{
				Symbol:     symbol,
				Interval:   interval,
				GapStartMs: openTimes[i-1] + intervalMs,
				GapEndMs:   openTimes[i] - 1,
				DetectedAt: now,
			})
		}
	}
	return out
}

// IntervalMs converts a Binance interval string to milliseconds.
// Returns an error for unrecognised intervals so a typo doesn't silently
// pass through to gap-detection math.
func IntervalMs(interval string) (int64, error) {
	switch interval {
	case "1s":
		return 1_000, nil
	case "1m":
		return 60_000, nil
	case "3m":
		return 180_000, nil
	case "5m":
		return 300_000, nil
	case "15m":
		return 900_000, nil
	case "30m":
		return 1_800_000, nil
	case "1h":
		return 3_600_000, nil
	case "2h":
		return 7_200_000, nil
	case "4h":
		return 14_400_000, nil
	case "6h":
		return 21_600_000, nil
	case "8h":
		return 28_800_000, nil
	case "12h":
		return 43_200_000, nil
	case "1d":
		return 86_400_000, nil
	case "3d":
		return 259_200_000, nil
	case "1w":
		return 604_800_000, nil
	}
	return 0, fmt.Errorf("unknown interval %q", interval)
}

// ---- pure helpers (no DB / no network) ----

type yearMonth struct{ Year, Month int }

// monthRange returns every (year, month) tuple in [start, end] inclusive
// (UTC, day-of-month ignored). Order: ascending.
func monthRange(start, end time.Time) []yearMonth {
	cur := time.Date(start.Year(), start.Month(), 1, 0, 0, 0, 0, time.UTC)
	stop := time.Date(end.Year(), end.Month(), 1, 0, 0, 0, 0, time.UTC)
	var out []yearMonth
	for !cur.After(stop) {
		out = append(out, yearMonth{Year: cur.Year(), Month: int(cur.Month())})
		cur = cur.AddDate(0, 1, 0)
	}
	return out
}

// filterByOpenTime returns rows whose OpenTime ∈ [startMs, endMs].
func filterByOpenTime(rows []KlineRow, startMs, endMs int64) []KlineRow {
	out := rows[:0:0]
	for _, r := range rows {
		if r.OpenTime >= startMs && r.OpenTime <= endMs {
			out = append(out, r)
		}
	}
	return out
}
