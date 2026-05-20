package store

import (
	"context"
	"errors"
	"fmt"

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

	return db, nil
}
