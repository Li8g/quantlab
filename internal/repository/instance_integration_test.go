//go:build integration

// Integration tests for InstanceRepo against a live Postgres instance.
// Run with:
//
//	go test -tags=integration ./internal/repository/ -args -config=/absolute/path/to/config.yaml
package repository

import (
	"context"
	"errors"
	"testing"

	"quantlab/internal/api"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func openInstanceTestDB(t *testing.T) (*InstanceRepo, context.Context) {
	t.Helper()
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
	return NewInstanceRepo(db), context.Background()
}

// TestInstanceRepo_ListByAccount_ExcludesRetired pins the retired-exclusion
// contract: a retired instance is terminal and must NOT surface in the
// account's reconciliation scope (stale ledger would fabricate drift and
// auto-freeze). After the one-per-account constraint (uq_inst_one_per_account),
// only one non-retired instance per account is allowed; the test reflects that.
func TestInstanceRepo_ListByAccount_ExcludesRetired(t *testing.T) {
	repo, ctx := openInstanceTestDB(t)

	const mainAcct = "lba-main-acct"
	const otherAcct = "lba-other-acct"
	cleanup := func() {
		cfg, _ := config.Load(*configPath)
		db, _ := store.NewDB(context.Background(), cfg)
		_ = db.Where("account_id IN ?", []string{mainAcct, otherAcct}).Delete(&store.StrategyInstance{}).Error
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	mk := func(id, acct string, st store.InstanceStatus) {
		if err := repo.Create(ctx, &store.StrategyInstance{
			InstanceID: id, StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
			AccountID: acct, OwnerUserID: 1, Status: st,
		}); err != nil {
			t.Fatalf("create %s: %v", id, err)
		}
	}

	// One non-retired + one retired on the target account.
	mk("lba-live", mainAcct, store.InstanceStatusLive)
	mk("lba-retired", mainAcct, store.InstanceStatusRetired)
	// One non-retired on a different account (must not appear).
	mk("lba-other", otherAcct, store.InstanceStatusIdle)

	got, err := repo.ListByAccount(ctx, mainAcct)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (only lba-live); got %v", len(got), got)
	}
	if got[0].InstanceID != "lba-live" {
		t.Errorf("InstanceID = %q, want lba-live", got[0].InstanceID)
	}
}

// TestInstanceRepo_Create_OnePerAccount proves that a second non-retired
// instance on the same (owner_user_id, account_id) is rejected with
// api.ErrAccountActiveInstanceExists.
func TestInstanceRepo_Create_OnePerAccount(t *testing.T) {
	repo, ctx := openInstanceTestDB(t)

	const acct = "opa-test-acct"
	cleanup := func() {
		cfg, _ := config.Load(*configPath)
		db, _ := store.NewDB(context.Background(), cfg)
		_ = db.Where("account_id = ?", acct).Delete(&store.StrategyInstance{}).Error
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	first := &store.StrategyInstance{
		InstanceID: "opa-first", StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
		AccountID: acct, OwnerUserID: 1, Status: store.InstanceStatusIdle,
	}
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("first Create: %v", err)
	}

	// Second non-retired instance on the same account must be rejected.
	second := &store.StrategyInstance{
		InstanceID: "opa-second", StrategyID: "sigmoid_v1", Pair: "ETHUSDT",
		AccountID: acct, OwnerUserID: 1, Status: store.InstanceStatusIdle,
	}
	err := repo.Create(ctx, second)
	if !errors.Is(err, api.ErrAccountActiveInstanceExists) {
		t.Fatalf("second Create err = %v, want ErrAccountActiveInstanceExists", err)
	}
}

// TestInstanceRepo_Create_RetiredAllowsNew proves that a retired instance does
// not block creating a new non-retired instance on the same account.
func TestInstanceRepo_Create_RetiredAllowsNew(t *testing.T) {
	repo, ctx := openInstanceTestDB(t)

	const acct = "ran-test-acct"
	cleanup := func() {
		cfg, _ := config.Load(*configPath)
		db, _ := store.NewDB(context.Background(), cfg)
		_ = db.Where("account_id = ?", acct).Delete(&store.StrategyInstance{}).Error
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	// Create a retired instance.
	retired := &store.StrategyInstance{
		InstanceID: "ran-retired", StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
		AccountID: acct, OwnerUserID: 1, Status: store.InstanceStatusRetired,
	}
	if err := repo.Create(ctx, retired); err != nil {
		t.Fatalf("retired Create: %v", err)
	}

	// A new non-retired instance on the same account must succeed.
	next := &store.StrategyInstance{
		InstanceID: "ran-new", StrategyID: "sigmoid_v1", Pair: "BTCUSDT",
		AccountID: acct, OwnerUserID: 1, Status: store.InstanceStatusIdle,
	}
	if err := repo.Create(ctx, next); err != nil {
		t.Fatalf("new Create after retired: %v", err)
	}
}
