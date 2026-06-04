package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"

	"github.com/pressly/goose/v3"
)

// migrationsFS embeds the versioned goose SQL so migrations ship inside the
// cmd/saas binary — prod deploys never depend on an external goose CLI or a
// checked-out copy of this directory (R7 in docs/saas-schema-migration-draft.md).
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// RunMigrations applies all pending goose migrations against sqlDB.
//
// sqlDB MUST be a connection pool dedicated to migrations, not the live app
// pool: this function pins it to a single connection (so the SET below sticks
// for every statement goose runs) and leaves lock_timeout set on it. Open it,
// hand it here, close it — see migrate_drift_test.go and cmd/saas for the
// expected call shape.
//
// lock_timeout='3s' bounds how long any statement waits to *acquire* a lock;
// a migration stuck behind a long-running transaction fails fast instead of
// queueing live traffic behind an ungranted ACCESS EXCLUSIVE lock. It is NOT
// statement_timeout — a legitimately slow migration (large backfill, hypertable
// cross-chunk ALTER) must still be allowed to run to completion, so no tight
// global statement_timeout is set here (per the draft's down-stream guardrail
// decision; use SET LOCAL inside a migration when a specific one needs a cap).
func RunMigrations(ctx context.Context, sqlDB *sql.DB) error {
	sqlDB.SetMaxOpenConns(1)

	goose.SetBaseFS(migrationsFS)
	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("store.RunMigrations: set dialect: %w", err)
	}
	if _, err := sqlDB.ExecContext(ctx, "SET lock_timeout = '3s'"); err != nil {
		return fmt.Errorf("store.RunMigrations: set lock_timeout: %w", err)
	}
	if err := goose.UpContext(ctx, sqlDB, "migrations"); err != nil {
		return fmt.Errorf("store.RunMigrations: goose up: %w", err)
	}
	return nil
}
