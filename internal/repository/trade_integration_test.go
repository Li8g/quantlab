//go:build integration

// Integration test for TradeRepo.ListExecutionsForOrders against a live
// Postgres instance. Mirror of challenger_integration_test.go /
// sharpe_bank_integration_test.go. Run with:
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

func TestTradeRepo_ListExecutionsForOrders(t *testing.T) {
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

	// Three orders; co-A and co-B are requested, co-C is the
	// negative control (its fills must NOT come back).
	orders := []store.TradeRecord{
		{ClientOrderID: "ti-co-A", InstanceID: "ti-inst", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
		{ClientOrderID: "ti-co-B", InstanceID: "ti-inst", Symbol: "BTCUSDT", Side: "sell", OrderType: "limit", QuantityUSD: 50, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
		{ClientOrderID: "ti-co-C", InstanceID: "ti-inst", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 10, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
	}
	// co-A has two fills out of insertion order so we can prove the
	// query sorts by filled_at_exchange_ms ASC, not by insertion.
	fills := []store.SpotExecution{
		{ClientOrderID: "ti-co-A", ExchangeOrderID: "ti-ex-A2", FillQuantity: 0.2, FillPrice: 60010, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 200},
		{ClientOrderID: "ti-co-A", ExchangeOrderID: "ti-ex-A1", FillQuantity: 0.3, FillPrice: 60000, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 100},
		{ClientOrderID: "ti-co-B", ExchangeOrderID: "ti-ex-B1", FillQuantity: 0.1, FillPrice: 61000, FillFeeAsset: "USDT", FillFeeAmount: 0.05, FilledAtExchangeMs: 150},
		{ClientOrderID: "ti-co-C", ExchangeOrderID: "ti-ex-C1", FillQuantity: 0.9, FillPrice: 59000, FillFeeAsset: "USDT", FillFeeAmount: 0.4, FilledAtExchangeMs: 50},
	}

	cleanup := func() {
		_ = db.Where("client_order_id LIKE ?", "ti-%").Delete(&store.SpotExecution{}).Error
		_ = db.Where("client_order_id LIKE ?", "ti-%").Delete(&store.TradeRecord{}).Error
	}
	cleanup()          // clear leftovers from a prior failed run
	t.Cleanup(cleanup) // and clean up after ourselves

	for i := range orders {
		if err := repo.InsertTradeRecord(ctx, &orders[i]); err != nil {
			t.Fatalf("InsertTradeRecord %s: %v", orders[i].ClientOrderID, err)
		}
	}
	for i := range fills {
		if err := repo.InsertSpotExecution(ctx, &fills[i]); err != nil {
			t.Fatalf("InsertSpotExecution %s: %v", fills[i].ExchangeOrderID, err)
		}
	}

	// Empty input short-circuits without touching the DB.
	got, err := repo.ListExecutionsForOrders(ctx, nil)
	if err != nil {
		t.Fatalf("ListExecutionsForOrders(nil): %v", err)
	}
	if got != nil {
		t.Errorf("empty input: got %v, want nil", got)
	}

	// Request co-A and co-B; co-C must be excluded.
	got, err = repo.ListExecutionsForOrders(ctx, []string{"ti-co-A", "ti-co-B"})
	if err != nil {
		t.Fatalf("ListExecutionsForOrders: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3 (2×co-A + 1×co-B); got %+v", len(got), got)
	}

	// Global ordering must be filled_at_exchange_ms ASC: 100, 150, 200.
	wantOrder := []int64{100, 150, 200}
	byOrder := map[string]int{}
	for i, ex := range got {
		if ex.FilledAtExchangeMs != wantOrder[i] {
			t.Errorf("row %d FilledAtExchangeMs = %d, want %d", i, ex.FilledAtExchangeMs, wantOrder[i])
		}
		if ex.ClientOrderID == "ti-co-C" {
			t.Errorf("row %d leaked co-C, which was not requested", i)
		}
		byOrder[ex.ClientOrderID]++
	}
	if byOrder["ti-co-A"] != 2 {
		t.Errorf("co-A fills = %d, want 2", byOrder["ti-co-A"])
	}
	if byOrder["ti-co-B"] != 1 {
		t.Errorf("co-B fills = %d, want 1", byOrder["ti-co-B"])
	}
}
