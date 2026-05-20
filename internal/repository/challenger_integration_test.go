//go:build integration

// Integration test for ChallengerRepo against a live Postgres +
// TimescaleDB instance. Mirror of store/db_integration_test.go.
// Run with:
//
//	go test -tags=integration ./internal/repository/ \
//	    -args -config=/absolute/path/to/config.yaml
//
// Requires the database referenced by config.yaml to be reachable and
// pre-populated with the timescaledb extension.
package repository

import (
	"context"
	"flag"
	"testing"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

var configPath = flag.String("config", "config.yaml", "path to config.yaml for integration test")

func TestChallengerRepo_SaveRoundTrip(t *testing.T) {
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

	repo := NewChallengerRepo(db)
	pkg := samplePackage()
	const challengerID = "ch-integration-001"

	// Clean prior runs so the unique-index doesn't fail on retry.
	// Unscoped() bypasses GORM soft-delete: gene_records uses
	// gorm.Model and its uniqueIndex on challenger_id is not partial
	// on deleted_at, so soft-deleted rows would collide on re-insert.
	if err := db.Unscoped().Where("challenger_id = ?", challengerID).Delete(&store.GeneRecord{}).Error; err != nil {
		t.Fatalf("pre-clean: %v", err)
	}

	if err := repo.Save(context.Background(), challengerID, pkg); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read back via the lifted columns + the JSON blob.
	var rec store.GeneRecord
	if err := db.Where("challenger_id = ?", challengerID).First(&rec).Error; err != nil {
		t.Fatalf("read-back: %v", err)
	}
	if rec.StrategyID != pkg.Core.StrategyID {
		t.Errorf("StrategyID lift mismatch: %q vs %q", rec.StrategyID, pkg.Core.StrategyID)
	}
	if rec.ScoreTotal == nil || *rec.ScoreTotal != *pkg.Evaluation.ScoreTotal.Value {
		t.Errorf("ScoreTotal lift mismatch: %v vs %v",
			rec.ScoreTotal, pkg.Evaluation.ScoreTotal.Value)
	}
	if len(rec.FullPackageJSON) == 0 {
		t.Error("FullPackageJSON empty after round-trip")
	}

	// Re-run Save with the same challenger_id must fail (UNIQUE
	// constraint), proving the index is wired.
	if err := repo.Save(context.Background(), challengerID, pkg); err == nil {
		t.Error("expected unique-constraint violation on duplicate Save")
	}

	// Cleanup so subsequent runs are idempotent (Unscoped: see above).
	_ = db.Unscoped().Where("challenger_id = ?", challengerID).Delete(&store.GeneRecord{}).Error
	_ = resultpkg.Window6M // keep the resultpkg import live for go vet
}
