//go:build integration

// Integration test for store.NewDB against a live Postgres + TimescaleDB
// instance. Run with:
//
//	go test -tags=integration ./internal/saas/store/ \
//	    -args -config=/absolute/path/to/config.yaml
//
// Requires the database referenced by config.yaml to be reachable and
// pre-populated with the timescaledb extension (the test invokes
// CREATE EXTENSION IF NOT EXISTS as part of NewDB, but the OS-level
// install must already be in place — see docker/timescale image).
package store_test

import (
	"context"
	"flag"
	"testing"

	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

var configPath = flag.String("config", "config.yaml", "path to config.yaml for integration test")

func TestNewDB_HypertableAndExtension(t *testing.T) {
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}
	db, err := store.NewDB(context.Background(), cfg)
	if err != nil {
		t.Fatalf("NewDB: %v", err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	})

	var extVersion string
	if err := db.Raw(
		`SELECT extversion FROM pg_extension WHERE extname = 'timescaledb'`,
	).Scan(&extVersion).Error; err != nil {
		t.Fatalf("query extension: %v", err)
	}
	if extVersion == "" {
		t.Fatal("timescaledb extension not present after NewDB")
	}
	t.Logf("timescaledb version: %s", extVersion)

	var hypertable string
	if err := db.Raw(
		`SELECT hypertable_name FROM timescaledb_information.hypertables
		 WHERE hypertable_name = 'klines'`,
	).Scan(&hypertable).Error; err != nil {
		t.Fatalf("query hypertables: %v", err)
	}
	if hypertable != "klines" {
		t.Fatalf("klines not a hypertable: got %q", hypertable)
	}
}
