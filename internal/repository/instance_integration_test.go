//go:build integration

// Integration test for InstanceRepo.ListByAccount against a live Postgres
// instance. Pins the retired-exclusion contract: a retired instance is
// terminal and must NOT surface in the account's reconciliation scope, or its
// stale ledger would fabricate drift against the real exchange snapshot and
// auto-freeze the account. Run:
//
//	go test -tags=integration ./internal/repository/ -args -config=/absolute/path/to/config.yaml
package repository

import (
	"context"
	"testing"

	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func TestInstanceRepo_ListByAccount_ExcludesRetired(t *testing.T) {
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
	ctx := context.Background()
	repo := NewInstanceRepo(db)

	const acct = "lba-test-acct"
	cleanup := func() {
		_ = db.Where("account_id = ?", acct).Delete(&store.StrategyInstance{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	// Distinct pairs: a partial unique index forbids two active instances
	// sharing (strategy_id, pair, account_id).
	mk := func(id, pair string, st store.InstanceStatus) {
		if err := repo.Create(ctx, &store.StrategyInstance{
			InstanceID: id, StrategyID: "sigmoid_v1", Pair: pair,
			AccountID: acct, OwnerUserID: 1, Status: st,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}
	mk("lba-live", "BTCUSDT", store.InstanceStatusLive)
	mk("lba-idle", "ETHUSDT", store.InstanceStatusIdle)
	mk("lba-paused", "BNBUSDT", store.InstanceStatusPaused)
	mk("lba-retired", "SOLUSDT", store.InstanceStatusRetired)

	got, err := repo.ListByAccount(ctx, acct)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	ids := map[string]bool{}
	for _, r := range got {
		ids[r.InstanceID] = true
	}
	if !ids["lba-live"] || !ids["lba-idle"] || !ids["lba-paused"] {
		t.Errorf("non-retired instances missing: got %v", ids)
	}
	if ids["lba-retired"] {
		t.Errorf("retired instance leaked into reconciliation scope: %v", ids)
	}
	if len(got) != 3 {
		t.Errorf("len = %d, want 3 (live/idle/paused; retired excluded)", len(got))
	}
}
