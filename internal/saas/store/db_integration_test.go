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
	"strings"
	"testing"

	"quantlab/internal/resultpkg"
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

	for _, name := range []string{"klines", "portfolio_states"} {
		var hypertable string
		if err := db.Raw(
			`SELECT hypertable_name FROM timescaledb_information.hypertables
			 WHERE hypertable_name = ?`, name,
		).Scan(&hypertable).Error; err != nil {
			t.Fatalf("query hypertables(%s): %v", name, err)
		}
		if hypertable != name {
			t.Errorf("%s not a hypertable: got %q", name, hypertable)
		}
	}
}

// TestNewDB_ChampionUniqueIndexTableName guards against table-name drift in
// the hand-written `uq_champion_active` DDL. The ChampionHistory model has no
// TableName() override, so GORM AutoMigrate creates the GORM-default plural
// table `champion_histories`; an earlier DDL hard-coded the singular
// `champion_history`, which never exists, so NewDB aborted on every fresh
// migration with `relation "champion_history" does not exist`. The bug is
// invisible to model-based queries (db.Model(&ChampionHistory{}) resolves the
// plural name) — only the literal in the raw DDL drifts. This test pins the
// index to the real table: NewDB succeeding already proves the DDL ran, and
// the pg_indexes lookup proves it landed on champion_histories.
func TestNewDB_ChampionUniqueIndexTableName(t *testing.T) {
	cfg, err := config.Load(*configPath)
	if err != nil {
		t.Fatalf("load config %s: %v", *configPath, err)
	}
	// A buggy DDL (singular table) makes NewDB itself fail here.
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

	var tableName, indexDef string
	if err := db.Raw(
		`SELECT tablename, indexdef FROM pg_indexes WHERE indexname = 'uq_champion_active'`,
	).Row().Scan(&tableName, &indexDef); err != nil {
		t.Fatalf("query pg_indexes for uq_champion_active: %v", err)
	}
	if tableName != "champion_histories" {
		t.Errorf("uq_champion_active on wrong table: got %q, want champion_histories", tableName)
	}
	// Sanity-check the predicate so a future rewrite can't silently turn the
	// partial index into a full one (which would block legit re-promotes).
	if !strings.Contains(indexDef, "retired_at IS NULL") {
		t.Errorf("uq_champion_active lost its partial predicate: %q", indexDef)
	}
}

// TestStrategyInstance_PartialUniqueActive verifies that the partial
// unique index `(owner_user_id, strategy_id, pair, account_id) WHERE
// status != 'retired'` blocks two ACTIVE instances on the same key but
// allows a new active instance once an existing one transitions to
// retired. See docs/saas-tier2-schema-v1.md §4.2 / B5.
func TestStrategyInstance_PartialUniqueActive(t *testing.T) {
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

	const ownerID = uint(9_999_001)
	const strategyID = "tier2-test-strategy"
	const pair = "TIERBTC"
	const acct = "tier2-test-account"

	cleanup := func() {
		_ = db.Where("owner_user_id = ? AND strategy_id = ?", ownerID, strategyID).
			Delete(&store.StrategyInstance{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	mk := func(status store.InstanceStatus) *store.StrategyInstance {
		return &store.StrategyInstance{
			InstanceID:  store.NewULID(),
			StrategyID:  strategyID,
			Pair:        pair,
			AccountID:   acct,
			OwnerUserID: ownerID,
			Status:      status,
		}
	}

	// First active row should succeed.
	first := mk(store.InstanceStatusLive)
	if err := db.Create(first).Error; err != nil {
		t.Fatalf("first active create: %v", err)
	}

	// Second active row on same key must violate the partial unique.
	if err := db.Create(mk(store.InstanceStatusIdle)).Error; err == nil {
		t.Fatal("second active create succeeded; want unique violation")
	} else if !strings.Contains(strings.ToLower(err.Error()), "unique") {
		t.Errorf("expected unique-violation error, got: %v", err)
	}

	// Retire the first; same-key create then must succeed.
	if err := db.Model(first).Update("status", store.InstanceStatusRetired).Error; err != nil {
		t.Fatalf("update first to retired: %v", err)
	}
	third := mk(store.InstanceStatusLive)
	if err := db.Create(third).Error; err != nil {
		t.Errorf("post-retire create blocked: %v", err)
	}
}

// TestNewULID_Monotonic verifies the package's ULID generator produces
// monotonically increasing strings under rapid sequential calls. ULID
// MonotonicEntropy guarantees this for IDs minted in the same
// millisecond. See docs/saas-tier2-schema-v1.md §2.3 / CC3.
func TestNewULID_Monotonic(t *testing.T) {
	const n = 1024
	prev := store.NewULID()
	for i := 1; i < n; i++ {
		cur := store.NewULID()
		if cur <= prev {
			t.Fatalf("ULID not monotonic at %d: %s <= %s", i, cur, prev)
		}
		prev = cur
	}
}

// keep resultpkg import live; the package is intentionally pulled in
// so future Tier 2 integration tests can use TaskStatus/SpawnMode
// constants without extra import drift.
var _ = resultpkg.TaskStatusSucceeded
