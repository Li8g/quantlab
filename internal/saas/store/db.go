package store

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"quantlab/internal/saas/config"
)

// NewDB opens the Postgres connection, enables TimescaleDB, runs
// AutoMigrate on all GORM models, and turns klines into a hypertable.
//
// The hypertable creation is idempotent (if_not_exists=>TRUE). Compression
// is left to a deploy-time policy script per docs/agents/devops-expert.md;
// Phase 13 will add it.
//
// 铁律 4: AutoMigrate is for dev/lab only. For app_role=saas (production)
// the deploy must run Atlas migrations and AutoMigrate becomes a sanity
// check rather than the migration path — but enforcing that gate is the
// concern of the cmd/saas startup sequence (Phase 10), not this helper.
func NewDB(ctx context.Context, cfg *config.Config) (*gorm.DB, error) {
	if cfg == nil {
		return nil, errors.New("store.NewDB: cfg is nil")
	}

	db, err := gorm.Open(postgres.Open(cfg.Database.DSN()), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("store.NewDB: open postgres: %w", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("store.NewDB: get sql.DB: %w", err)
	}
	if cfg.Database.MaxOpenConns > 0 {
		sqlDB.SetMaxOpenConns(cfg.Database.MaxOpenConns)
	}
	if cfg.Database.MaxIdleConns > 0 {
		sqlDB.SetMaxIdleConns(cfg.Database.MaxIdleConns)
	}

	if err := db.WithContext(ctx).
		Exec(`CREATE EXTENSION IF NOT EXISTS timescaledb`).Error; err != nil {
		return nil, fmt.Errorf("store.NewDB: enable timescaledb: %w", err)
	}

	if err := db.WithContext(ctx).AutoMigrate(AllModels()...); err != nil {
		return nil, fmt.Errorf("store.NewDB: automigrate: %w", err)
	}

	// 7-day chunk on open_time (milliseconds). if_not_exists makes the call
	// idempotent across restarts.
	//
	// chunk_time_interval is interpolated directly (not parameterized): the
	// value is a compile-time constant with no injection risk, and
	// create_hypertable is polymorphic — a bound `?` placeholder arrives as
	// PG type `unknown` and trips
	// `ERROR: could not determine polymorphic type` (SQLSTATE 42804).
	const chunk7DaysMs = int64(7) * 24 * 60 * 60 * 1000
	hyperSQL := fmt.Sprintf(
		`SELECT create_hypertable('klines', 'open_time',
			if_not_exists => TRUE,
			chunk_time_interval => %d::bigint)`,
		chunk7DaysMs,
	)
	if err := db.WithContext(ctx).Exec(hyperSQL).Error; err != nil {
		return nil, fmt.Errorf("store.NewDB: create_hypertable klines: %w", err)
	}

	// portfolio_states hypertable (30-day chunks). Per
	// docs/saas-tier2-schema-v1.md §5.1 / C1: enabling at table-create
	// time avoids a stop-the-world migration when row counts grow in
	// live trading.
	const chunk30DaysMs = int64(30) * 24 * 60 * 60 * 1000
	psHyperSQL := fmt.Sprintf(
		`SELECT create_hypertable('portfolio_states', 'now_ms',
			if_not_exists => TRUE,
			migrate_data  => TRUE,
			chunk_time_interval => %d::bigint)`,
		chunk30DaysMs,
	)
	if err := db.WithContext(ctx).Exec(psHyperSQL).Error; err != nil {
		return nil, fmt.Errorf("store.NewDB: create_hypertable portfolio_states: %w", err)
	}

	// Partial unique index on strategy_instances per §4.2 / B5:
	// same (user, strategy, pair, account) can only have one active
	// instance, but retired instances don't block re-creation.
	// GORM tags cannot express partial unique, so we DDL explicitly.
	const psUniqueSQL = `CREATE UNIQUE INDEX IF NOT EXISTS idx_inst_unique_active
		ON strategy_instances (owner_user_id, strategy_id, pair, account_id)
		WHERE status != 'retired'`
	if err := db.WithContext(ctx).Exec(psUniqueSQL).Error; err != nil {
		return nil, fmt.Errorf("store.NewDB: partial unique strategy_instances: %w", err)
	}

	// Partial unique index on import_jobs per docs/phase9-data-import-v1.md
	// §2.1: a (symbol, interval) pair can only have one active import at a
	// time (the orchestrator delete+rewrites that range's gap rows, so
	// concurrent imports of the same pair corrupt each other). Active =
	// queued|running; terminal jobs don't block re-import. GORM tags can't
	// express partial unique, so DDL explicitly.
	const importJobUniqueSQL = `CREATE UNIQUE INDEX IF NOT EXISTS uq_import_jobs_active
		ON import_jobs (symbol, interval)
		WHERE status IN ('queued', 'running')`
	if err := db.WithContext(ctx).Exec(importJobUniqueSQL).Error; err != nil {
		return nil, fmt.Errorf("store.NewDB: partial unique import_jobs: %w", err)
	}

	// Partial unique index on champion_history: at most one ACTIVE champion
	// per (strategy_id, pair). "Active" = not retired AND not GORM-soft-
	// deleted, matching ChampionRepo.Promote's count predicate so the DB
	// agrees with the application's notion of "active". This is the DB-level
	// backstop for the count-then-insert in Promote: under READ COMMITTED two
	// concurrent promotes both read activeOther=0 and would otherwise both
	// commit a second active row. The unique index makes the loser's INSERT
	// fail at commit (Promote maps it to ErrActiveChampionExists). GORM tags
	// can't express partial unique, so DDL explicitly.
	//
	// Precondition: no pre-existing duplicates (CREATE UNIQUE INDEX aborts on
	// them). assertNoDuplicateActiveChampions surfaces an actionable error
	// instead of Postgres's opaque one; scripts/preflight_champion_dup_check.sql
	// is the standalone operator diagnostic.
	if err := assertNoDuplicateActiveChampions(ctx, db); err != nil {
		return nil, err
	}
	const championUniqueSQL = `CREATE UNIQUE INDEX IF NOT EXISTS uq_champion_active
		ON champion_history (strategy_id, pair)
		WHERE retired_at IS NULL AND deleted_at IS NULL`
	if err := db.WithContext(ctx).Exec(championUniqueSQL).Error; err != nil {
		return nil, fmt.Errorf("store.NewDB: partial unique champion_history: %w", err)
	}

	return db, nil
}

// assertNoDuplicateActiveChampions fails fast if champion_history already
// holds more than one active row for any (strategy_id, pair). CREATE UNIQUE
// INDEX over such duplicates aborts with an opaque "could not create unique
// index" error; this turns that into a precise, operator-actionable message.
// GORM's Model query auto-appends `deleted_at IS NULL` (soft-delete), so the
// predicate here matches the index's WHERE clause.
func assertNoDuplicateActiveChampions(ctx context.Context, db *gorm.DB) error {
	type dupRow struct {
		StrategyID  string
		Pair        string
		ActiveCount int64
	}
	var dups []dupRow
	if err := db.WithContext(ctx).
		Model(&ChampionHistory{}).
		Select("strategy_id, pair, count(*) AS active_count").
		Where("retired_at IS NULL").
		Group("strategy_id, pair").
		Having("count(*) > 1").
		Scan(&dups).Error; err != nil {
		return fmt.Errorf("store.NewDB: check duplicate active champions: %w", err)
	}
	if len(dups) == 0 {
		return nil
	}
	var b strings.Builder
	for _, d := range dups {
		fmt.Fprintf(&b, " (strategy_id=%q pair=%q: %d active)", d.StrategyID, d.Pair, d.ActiveCount)
	}
	return fmt.Errorf(
		"store.NewDB: cannot create uq_champion_active — %d (strategy_id,pair) group(s) already have >1 active champion:%s; retire the stale ones (see scripts/preflight_champion_dup_check.sql) before restarting",
		len(dups), b.String(),
	)
}
