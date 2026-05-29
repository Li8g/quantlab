//go:build integration

// Integration tests for the Phase 8 delta_report persistence against a
// live Postgres instance: TradeRepo.ExecutionExists (the fill-dedup query)
// and ReconRepo inserts (proving AutoMigrate created the two forensic
// tables and rows round-trip). Mirror of trade_integration_test.go. Run:
//
//	go test -tags=integration ./internal/repository/ \
//	    -args -config=/absolute/path/to/config.yaml
//
// reuses the configPath flag defined in challenger_integration_test.go.
package repository

import (
	"context"
	"testing"

	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
)

func TestTradeRepo_ExecutionExists(t *testing.T) {
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

	repo := NewTradeRepo(db)
	ctx := context.Background()

	cleanup := func() {
		_ = db.Where("client_order_id LIKE ?", "re-%").Delete(&store.SpotExecution{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	ex := &store.SpotExecution{
		ClientOrderID: "re-co-1", ExchangeOrderID: "re-ex-1",
		FillQuantity: 0.1, FillPrice: 60000, FillFeeAsset: "USDT",
		FillFeeAmount: 0.06, FilledAtExchangeMs: 1234,
	}
	if err := repo.InsertSpotExecution(ctx, ex); err != nil {
		t.Fatalf("InsertSpotExecution: %v", err)
	}

	// Exact (client_order_id, filled_at_exchange_ms) match → exists.
	got, err := repo.ExecutionExists(ctx, "re-co-1", 1234)
	if err != nil {
		t.Fatalf("ExecutionExists: %v", err)
	}
	if !got {
		t.Error("want exists=true for the inserted fill")
	}

	// Same order, different fill time → not a dup (a second partial fill).
	got, err = repo.ExecutionExists(ctx, "re-co-1", 9999)
	if err != nil {
		t.Fatalf("ExecutionExists: %v", err)
	}
	if got {
		t.Error("want exists=false for a different filled_at_exchange_ms")
	}

	// Unknown order → not exists.
	got, err = repo.ExecutionExists(ctx, "re-co-unknown", 1234)
	if err != nil {
		t.Fatalf("ExecutionExists: %v", err)
	}
	if got {
		t.Error("want exists=false for an unknown client_order_id")
	}
}

func TestReconRepo_Inserts(t *testing.T) {
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

	repo := NewReconRepo(db)
	ctx := context.Background()

	cleanup := func() {
		_ = db.Where("account_id = ?", "re-acct").Delete(&store.ReconciliationDiscrepancy{}).Error
		_ = db.Where("account_id = ?", "re-acct").Delete(&store.AgentError{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	d := &store.ReconciliationDiscrepancy{
		AccountID: "re-acct", InstanceID: "re-inst", Asset: "BTC",
		ExpectedAmount: 0.5, ActualAmount: 0.6, DiffAmount: 0.1,
		DriftBps: 1818.18, ReportedAtMs: 1000, DetectedAtMs: 1001,
	}
	if err := repo.InsertDiscrepancy(ctx, d); err != nil {
		t.Fatalf("InsertDiscrepancy: %v", err)
	}
	var dCount int64
	db.Model(&store.ReconciliationDiscrepancy{}).Where("account_id = ?", "re-acct").Count(&dCount)
	if dCount != 1 {
		t.Errorf("discrepancy rows = %d, want 1", dCount)
	}

	e := &store.AgentError{
		AccountID: "re-acct", InstanceID: "re-inst",
		Code: "exchange_rate_limit", Message: "429 from binance",
		OccurredAtMs: 2000, ReportedAtMs: 2001,
	}
	if err := repo.InsertAgentError(ctx, e); err != nil {
		t.Fatalf("InsertAgentError: %v", err)
	}
	var eCount int64
	db.Model(&store.AgentError{}).Where("account_id = ?", "re-acct").Count(&eCount)
	if eCount != 1 {
		t.Errorf("agent_error rows = %d, want 1", eCount)
	}
}
