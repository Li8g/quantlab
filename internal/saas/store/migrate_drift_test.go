//go:build integration

// Drift guard — the load-bearing wall of the goose migration scheme
// (docs/saas-schema-migration-draft.md §决策摘要 / D7).
//
// dev/lab build their schema with GORM AutoMigrate; prod (app_role=saas)
// builds it with the versioned goose migrations. Those are two source-of-truth
// representations of the same schema, kept honest only by this test: it builds
// one database each way and asserts their `pg_dump --schema-only` output is
// byte-identical after normalization. A new GORM struct field with no matching
// migration (or a migration that drifts from the struct) fails here.
//
// This is the same equivalence check that validated 00001_baseline.sql by hand;
// it is kept as a permanent CI gate so the two paths can never silently diverge.
//
// Run with (needs a reachable Postgres+TimescaleDB superuser able to CREATE
// DATABASE, and pg_dump on PATH):
//
//	go test -tags=integration ./internal/saas/store/ \
//	    -run TestMigrationsMatchAutoMigrate -args -config=/abs/config.yaml
package store_test

import (
	"context"
	"database/sql"
	"fmt"
	"os/exec"
	"strings"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

// TestMigrationsMatchAutoMigrate builds two throwaway databases — one via
// AutoMigrate (app_role=dev), one via goose (app_role=saas) — and diffs their
// schema dumps. The diff must be empty.
func TestMigrationsMatchAutoMigrate(t *testing.T) {
	if _, err := exec.LookPath("pg_dump"); err != nil {
		t.Skip("pg_dump not on PATH; skipping schema drift check")
	}

	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}

	ctx := context.Background()
	suffix := time.Now().UnixNano()
	amName := fmt.Sprintf("qldrift_am_%d", suffix)
	gsName := fmt.Sprintf("qldrift_gs_%d", suffix)

	admin := openMaintenance(t, cfg)
	createScratchDB(t, ctx, admin, amName)
	createScratchDB(t, ctx, admin, gsName)

	// AutoMigrate path: app_role=dev runs CREATE EXTENSION + AutoMigrate +
	// the raw hypertable/partial-index DDL in db.go.
	amCfg := withDatabase(cfg, amName, config.AppRoleDev)
	if amDB, err := store.NewDB(ctx, amCfg); err != nil {
		t.Fatalf("AutoMigrate (dev) NewDB: %v", err)
	} else {
		closeGorm(amDB)
	}

	// goose path: app_role=saas runs the versioned migrations only.
	gsCfg := withDatabase(cfg, gsName, config.AppRoleSaaS)
	if gsDB, err := store.NewDB(ctx, gsCfg); err != nil {
		t.Fatalf("goose (saas) NewDB: %v", err)
	} else {
		closeGorm(gsDB)
	}

	amDump := dumpSchema(t, cfg, amName)
	gsDump := dumpSchema(t, cfg, gsName)

	if amDump != gsDump {
		t.Errorf("schema drift between AutoMigrate and goose baseline:\n%s",
			lineDiff(amDump, gsDump))
	}
}

// openMaintenance connects to the cluster's default `postgres` database so the
// test can CREATE/DROP the scratch databases (you cannot drop the database you
// are connected to).
func openMaintenance(t *testing.T, cfg *config.Config) *sql.DB {
	t.Helper()
	db, err := sql.Open("pgx", dsnFor(cfg, "postgres"))
	if err != nil {
		t.Fatalf("open maintenance db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.Ping(); err != nil {
		t.Fatalf("ping maintenance db (is Postgres reachable per %s?): %v", *configPath, err)
	}
	return db
}

func createScratchDB(t *testing.T, ctx context.Context, admin *sql.DB, name string) {
	t.Helper()
	// Identifiers are test-generated (qldrift_xx_<nanos>), no injection surface.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create database %s: %v", name, err)
	}
	t.Cleanup(func() {
		// Terminate stray backends, then drop. Best-effort cleanup.
		_, _ = admin.Exec(
			"SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname = $1", name)
		if _, err := admin.Exec("DROP DATABASE IF EXISTS " + name); err != nil {
			t.Logf("cleanup: drop database %s: %v", name, err)
		}
	})
}

// dumpSchema runs `pg_dump --schema-only` against the named database and
// returns the normalized DDL (comments, ownership, the session SET preamble,
// goose's own bookkeeping table, and blank lines removed) so two semantically
// identical schemas compare equal regardless of cosmetic dump noise.
func dumpSchema(t *testing.T, cfg *config.Config, dbName string) string {
	t.Helper()
	cmd := exec.Command("pg_dump",
		"-h", cfg.Database.Host,
		"-p", fmt.Sprintf("%d", cfg.Database.Port),
		"-U", cfg.Database.User,
		"-d", dbName,
		"--schema-only", "--no-owner", "--no-privileges",
		"--exclude-table=goose_db_version",
		"--exclude-table=goose_db_version_id_seq",
	)
	cmd.Env = append(cmd.Environ(), "PGPASSWORD="+cfg.Database.Password)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("pg_dump %s: %v", dbName, err)
	}
	return normalizeDump(string(out))
}

func normalizeDump(dump string) string {
	var keep []string
	for _, line := range strings.Split(dump, "\n") {
		switch {
		case line == "",
			strings.HasPrefix(line, "--"),
			strings.HasPrefix(line, "\\"),
			strings.HasPrefix(line, "SET "),
			strings.HasPrefix(line, "SELECT pg_catalog.set_config"):
			continue
		}
		keep = append(keep, line)
	}
	return strings.Join(keep, "\n")
}

// lineDiff renders a compact, line-oriented diff for the failure message.
func lineDiff(a, b string) string {
	al, bl := strings.Split(a, "\n"), strings.Split(b, "\n")
	var sb strings.Builder
	n := len(al)
	if len(bl) > n {
		n = len(bl)
	}
	for i := 0; i < n; i++ {
		var x, y string
		if i < len(al) {
			x = al[i]
		}
		if i < len(bl) {
			y = bl[i]
		}
		if x != y {
			fmt.Fprintf(&sb, "- (automigrate) %s\n+ (goose)       %s\n", x, y)
		}
	}
	return sb.String()
}

func withDatabase(cfg *config.Config, dbName string, role config.AppRole) *config.Config {
	c := *cfg // DatabaseConfig is a value field, so this copy is independent
	c.AppRole = role
	c.Database.Database = dbName
	return &c
}

func dsnFor(cfg *config.Config, dbName string) string {
	d := cfg.Database
	return fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=%s",
		d.Host, d.Port, d.User, d.Password, dbName, d.SSLMode)
}

func closeGorm(db interface{ DB() (*sql.DB, error) }) {
	if sqlDB, err := db.DB(); err == nil && sqlDB != nil {
		_ = sqlDB.Close()
	}
}
