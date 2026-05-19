//go:build integration

// Integration test for SharpeBankRepo against a live Postgres +
// TimescaleDB instance. Mirror of challenger_integration_test.go and
// store/db_integration_test.go.
//
//	go test -tags=integration ./internal/repository/ \
//	    -args -config=/absolute/path/to/config.yaml
package repository

import (
	"context"
	"math"
	"testing"

	"quantlab/internal/resultpkg"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
	"quantlab/internal/verification"
)

// reuses configPath flag defined in challenger_integration_test.go

func TestSharpeBankRepo_AddThenStatsRoundTrip(t *testing.T) {
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

	const (
		strategyID = "sharpe-test-strategy"
		pairID     = "BTCUSDT"
	)
	// Wipe any leftover rows from previous runs of the same key
	// so the row counts in this test are predictable.
	if err := db.Where("strategy_id = ? AND pair_id = ?", strategyID, pairID).
		Delete(&store.SharpeBank{}).Error; err != nil {
		t.Fatalf("pre-clean: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Where("strategy_id = ? AND pair_id = ?", strategyID, pairID).
			Delete(&store.SharpeBank{}).Error
	})

	repo := NewSharpeBankRepo(db)

	// Empty bank: N=0, no DSR computable.
	stats, err := repo.Stats(context.Background(), strategyID, pairID)
	if err != nil {
		t.Fatalf("Stats on empty bank: %v", err)
	}
	if stats.N != 0 {
		t.Errorf("empty bank: N = %d, want 0", stats.N)
	}

	// Add 5 entries with known Sharpes: 1, 2, 3, 4, 5 → mean=3, pop-var=2.
	sharpes := []float64{1, 2, 3, 4, 5}
	for i, sr := range sharpes {
		entry := SharpeBankEntry{
			ChallengerID: "ch-sharpe-" + string(rune('A'+i)),
			SpawnMode:    resultpkg.SpawnModeRandomOnce,
			Stats: verification.SharpeStats{
				ObservedSharpe: sr,
				Skew:           -0.1,
				ExcessKurt:     0.3,
				HorizonT:       365,
			},
		}
		if err := repo.Add(context.Background(), strategyID, pairID, entry); err != nil {
			t.Fatalf("Add #%d: %v", i, err)
		}
	}

	stats, err = repo.Stats(context.Background(), strategyID, pairID)
	if err != nil {
		t.Fatalf("Stats after Adds: %v", err)
	}
	if stats.N != 5 {
		t.Errorf("N = %d, want 5", stats.N)
	}
	if math.Abs(stats.SharpeMean-3.0) > 1e-9 {
		t.Errorf("Mean = %v, want 3.0", stats.SharpeMean)
	}
	if math.Abs(stats.SharpeVariance-2.0) > 1e-9 {
		t.Errorf("Variance = %v, want 2.0 (population)", stats.SharpeVariance)
	}

	// Now N=5 ≥ MinTrialsForDSR → DSR is computable, not NaN.
	dsr := verification.ComputeDSR(
		3.5, // a new challenger above the bank mean
		stats.SharpeVariance,
		stats.N,
		365,
		-0.1, 0.3,
	)
	if math.IsNaN(dsr) {
		t.Errorf("DSR = NaN at N=5 with var=2 — should be computable")
	}
}

func TestSharpeBankRepo_AddRejectsEmptyKeys(t *testing.T) {
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
	repo := NewSharpeBankRepo(db)
	if err := repo.Add(context.Background(), "", "BTCUSDT", SharpeBankEntry{ChallengerID: "x"}); err == nil {
		t.Error("Add with empty strategyID should error")
	}
	if err := repo.Add(context.Background(), "s", "", SharpeBankEntry{ChallengerID: "x"}); err == nil {
		t.Error("Add with empty pairID should error")
	}
	if err := repo.Add(context.Background(), "s", "BTCUSDT", SharpeBankEntry{}); err == nil {
		t.Error("Add with empty ChallengerID should error")
	}
}
