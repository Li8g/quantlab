//go:build integration

// Integration test for the genesis-funding orchestration in agentmsg.go
// against a live Postgres instance. The pure helpers (buildSeedPortfolio,
// reconcilePositions, maxFlaggedDriftBps) are unit-tested in
// agentmsg_test.go; this pins the *wiring* across handleDeltaReport →
// reconcile → fundInstance → the InstanceRepo/PortfolioRepo/ReconRepo
// writes, which only exercises against a real DB (the repos are concrete
// types, not interfaces — see the project memory on cmd/saas integration
// harness). Run:
//
//	go test -tags=integration ./cmd/saas/ -args -config=/absolute/path/to/config.yaml
//
// The flag is local to this package (cmd/saas has no other integration test).
package main

import (
	"context"
	"flag"
	"testing"

	"quantlab/internal/repository"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

var configPath = flag.String("config", "config.yaml", "path to config.yaml for integration test")

const gfAccount = "gf-test-acct"

func pos(sym, free, locked string) wire.Position {
	return wire.Position{Symbol: sym, FreeDecimal: free, LockedDecimal: locked}
}

// TestGenesisFunding_Integration walks a fresh instance through its first
// three delta_reports and pins the three behaviours the baseline=0 fix
// hinges on:
//
//	report #1 (junk faucet coin present) → instance funded from the exchange
//	  snapshot, ledger seeded with BTC/USDT only (junk excluded), and the
//	  genesis round records ZERO discrepancies (a never-funded $0 ledger is
//	  never reconciled against real holdings).
//	report #2 (clean, matches seed) → reconciled against the seeded baseline,
//	  still ZERO discrepancies (like-with-like, no false positive).
//	report #3 (real BTC drift) → exactly one BTC discrepancy flagged.
func TestGenesisFunding_Integration(t *testing.T) {
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

	instances := repository.NewInstanceRepo(db)
	portfolios := repository.NewPortfolioRepo(db)
	recon := repository.NewReconRepo(db)
	trades := repository.NewTradeRepo(db)

	const instanceID = "gf-inst-01"
	cleanup := func() {
		_ = db.Where("account_id = ?", gfAccount).Delete(&store.StrategyInstance{}).Error
		_ = db.Where("instance_id = ?", instanceID).Delete(&store.PortfolioState{}).Error
		_ = db.Where("account_id = ?", gfAccount).Delete(&store.ReconciliationDiscrepancy{}).Error
		_ = db.Where("account_id = ?", gfAccount).Delete(&store.AgentError{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	// Fresh instance: FundedAtMs NULL (never funded).
	inst := &store.StrategyInstance{
		InstanceID:  instanceID,
		StrategyID:  "sigmoid_v1",
		Pair:        "BTCUSDT",
		AccountID:   gfAccount,
		OwnerUserID: 1,
		Status:      store.InstanceStatusLive,
	}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	// killer nil ⇒ auto-freeze disabled; this test pins funding +
	// reconciliation, not the kill_switch trigger (covered elsewhere).
	h := newAgentMessageHandler(trades, instances, portfolios, recon, nil)

	discrepancyCount := func() int64 {
		var n int64
		db.Model(&store.ReconciliationDiscrepancy{}).
			Where("account_id = ?", gfAccount).Count(&n)
		return n
	}

	// ---- report #1: genesis round (junk faucet coin present) ----
	dr1 := &wire.DeltaReport{
		ReportedAtMs: 1000,
		Positions: []wire.Position{
			pos("BTC", "0.5", "0.0"),
			pos("USDT", "1000.0", "0.0"),
			pos("ACH", "9999.0", "0.0"), // faucet junk — must not seed, must not flag
		},
	}
	if err := h.handleDeltaReport(ctx, gfAccount, dr1); err != nil {
		t.Fatalf("handleDeltaReport #1: %v", err)
	}

	got, err := instances.Get(ctx, instanceID)
	if err != nil {
		t.Fatalf("get instance after #1: %v", err)
	}
	if got.FundedAtMs == nil {
		t.Fatal("instance not funded after genesis report")
	}
	fundedAt := *got.FundedAtMs

	seed, err := portfolios.Latest(ctx, instanceID)
	if err != nil {
		t.Fatalf("latest portfolio after #1: %v", err)
	}
	if seed == nil {
		t.Fatal("no seed portfolio written")
	}
	if seed.FloatBTC != 0.5 {
		t.Errorf("seed FloatBTC = %v, want 0.5 (whole base balance)", seed.FloatBTC)
	}
	if seed.USDT != 1000 {
		t.Errorf("seed USDT = %v, want 1000", seed.USDT)
	}
	// Junk faucet coin never enters the three-state ledger.
	if seed.DeadBTC != 0 || seed.ColdSealedBTC != 0 {
		t.Errorf("genesis non-zero dead/cold: %v/%v", seed.DeadBTC, seed.ColdSealedBTC)
	}
	if n := discrepancyCount(); n != 0 {
		t.Errorf("genesis round recorded %d discrepancies, want 0 (instance excluded)", n)
	}

	// ---- report #2: clean snapshot matching the seeded baseline ----
	dr2 := &wire.DeltaReport{
		ReportedAtMs: 2000,
		Positions: []wire.Position{
			pos("BTC", "0.5", "0.0"),
			pos("USDT", "1000.0", "0.0"),
		},
	}
	if err := h.handleDeltaReport(ctx, gfAccount, dr2); err != nil {
		t.Fatalf("handleDeltaReport #2: %v", err)
	}
	// Funding is idempotent: the second report must not re-stamp or re-seed.
	got, err = instances.Get(ctx, instanceID)
	if err != nil {
		t.Fatalf("get instance after #2: %v", err)
	}
	if got.FundedAtMs == nil || *got.FundedAtMs != fundedAt {
		t.Errorf("FundedAtMs re-stamped: %v, want stable %d", got.FundedAtMs, fundedAt)
	}
	if n := discrepancyCount(); n != 0 {
		t.Errorf("clean reconcile recorded %d discrepancies, want 0 (like-with-like)", n)
	}

	// ---- report #3: real BTC drift against the seeded baseline ----
	dr3 := &wire.DeltaReport{
		ReportedAtMs: 3000,
		Positions: []wire.Position{
			pos("BTC", "0.6", "0.0"), // +0.1 vs seeded 0.5 → ~1666 bps
			pos("USDT", "1000.0", "0.0"),
		},
	}
	if err := h.handleDeltaReport(ctx, gfAccount, dr3); err != nil {
		t.Fatalf("handleDeltaReport #3: %v", err)
	}
	rows, err := recon.ListDiscrepanciesForInstance(ctx, instanceID, 10)
	if err != nil {
		t.Fatalf("list discrepancies after #3: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("drift round recorded %d discrepancies, want 1", len(rows))
	}
	if rows[0].Asset != "BTC" {
		t.Errorf("flagged asset = %q, want BTC", rows[0].Asset)
	}
	if rows[0].ExpectedAmount != 0.5 || rows[0].ActualAmount != 0.6 {
		t.Errorf("drift expected/actual = %v/%v, want 0.5/0.6", rows[0].ExpectedAmount, rows[0].ActualAmount)
	}
}

// TestMarkFunded_IdempotentNullGuard pins the NULL-guard the genesis
// funding claim relies on: a second MarkFunded must not overwrite the first
// stamp (the safety net against two delta_reports racing to fund).
func TestMarkFunded_IdempotentNullGuard(t *testing.T) {
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
	instances := repository.NewInstanceRepo(db)

	const instanceID = "gf-inst-funded"
	cleanup := func() {
		_ = db.Where("account_id = ?", gfAccount).Delete(&store.StrategyInstance{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	inst := &store.StrategyInstance{
		InstanceID:  instanceID,
		StrategyID:  "sigmoid_v1",
		Pair:        "BTCUSDT",
		AccountID:   gfAccount,
		OwnerUserID: 1,
		Status:      store.InstanceStatusLive,
	}
	if err := instances.Create(ctx, inst); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	claimed1, err := instances.MarkFunded(ctx, instanceID, 111)
	if err != nil {
		t.Fatalf("MarkFunded first: %v", err)
	}
	if !claimed1 {
		t.Fatal("MarkFunded first: expected claimed=true")
	}
	claimed2, err := instances.MarkFunded(ctx, instanceID, 222)
	if err != nil {
		t.Fatalf("MarkFunded second: %v", err)
	}
	if claimed2 {
		t.Fatal("MarkFunded second: expected claimed=false (NULL guard blocks overwrite)")
	}
	got, err := instances.Get(ctx, instanceID)
	if err != nil {
		t.Fatalf("get instance: %v", err)
	}
	if got.FundedAtMs == nil || *got.FundedAtMs != 111 {
		t.Errorf("FundedAtMs = %v, want stable 111 (NULL-guard blocks overwrite)", got.FundedAtMs)
	}
}

// TestFundInstance_DoubleFundPreventsDuplicateSeed simulates two concurrent
// delta_reports both seeing FundedAtMs=NULL and both entering fundInstance.
// Only the first caller (the one that wins MarkFunded) must write the seed
// portfolio; the second must return nil without touching the DB.
func TestFundInstance_DoubleFundPreventsDuplicateSeed(t *testing.T) {
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
	instances := repository.NewInstanceRepo(db)
	portfolios := repository.NewPortfolioRepo(db)
	trades := repository.NewTradeRepo(db)
	recon := repository.NewReconRepo(db)

	const instanceID = "gf-inst-double"
	cleanup := func() {
		_ = db.Where("account_id = ?", gfAccount).Delete(&store.StrategyInstance{}).Error
		_ = db.Where("instance_id = ?", instanceID).Delete(&store.PortfolioState{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	base := &store.StrategyInstance{
		InstanceID:  instanceID,
		StrategyID:  "sigmoid_v1",
		Pair:        "BTCUSDT",
		AccountID:   gfAccount,
		OwnerUserID: 1,
		Status:      store.InstanceStatusLive,
	}
	if err := instances.Create(ctx, base); err != nil {
		t.Fatalf("create instance: %v", err)
	}

	h := newAgentMessageHandler(trades, instances, portfolios, recon, nil)
	actual := map[string]float64{"BTC": 0.5, "USDT": 1000.0}

	// First call: claims the slot and writes the seed.
	inst1 := *base
	if err := h.fundInstance(ctx, &inst1, actual, 1000); err != nil {
		t.Fatalf("fundInstance first: %v", err)
	}

	// Second call: FundedAtMs is still nil on this copy (race simulation).
	inst2 := *base
	if err := h.fundInstance(ctx, &inst2, actual, 1000); err != nil {
		t.Fatalf("fundInstance second: %v", err)
	}

	// Exactly one seed row must exist.
	var count int64
	db.Model(&store.PortfolioState{}).Where("instance_id = ?", instanceID).Count(&count)
	if count != 1 {
		t.Errorf("portfolio row count = %d, want 1 (double-fund prevented)", count)
	}
}
