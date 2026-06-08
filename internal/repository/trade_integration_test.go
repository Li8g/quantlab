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

	"gorm.io/gorm"

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

// openTradeTestDB is the shared harness for the ledger-writeback (③/①)
// integration tests below.
func openTradeTestDB(t *testing.T) (*TradeRepo, context.Context) {
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
	cleanup := func() {
		_ = db.Where("client_order_id LIKE ?", "nx-%").Delete(&store.SpotExecution{}).Error
		_ = db.Where("client_order_id LIKE ?", "nx-%").Delete(&store.TradeRecord{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)
	return NewTradeRepo(db), context.Background()
}

// TestTradeRepo_NewExecutionsForInstance proves the ③ writeback query:
// it scopes fills to one instance (join via client_order_id), projects the
// order Side, honors the `id > sinceID` watermark, and orders by id ASC.
func TestTradeRepo_NewExecutionsForInstance(t *testing.T) {
	repo, ctx := openTradeTestDB(t)

	orders := []store.TradeRecord{
		{ClientOrderID: "nx-co-A", InstanceID: "nx-inst-1", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
		{ClientOrderID: "nx-co-B", InstanceID: "nx-inst-1", Symbol: "BTCUSDT", Side: "sell", OrderType: "market", QuantityUSD: 50, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
		{ClientOrderID: "nx-co-Z", InstanceID: "nx-inst-2", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 10, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
	}
	for i := range orders {
		if err := repo.InsertTradeRecord(ctx, &orders[i]); err != nil {
			t.Fatalf("InsertTradeRecord %s: %v", orders[i].ClientOrderID, err)
		}
	}
	// FillPrice keys each fill to its side so we can assert the projection.
	fills := []store.SpotExecution{
		{ClientOrderID: "nx-co-A", ExchangeOrderID: "nx-ex-A", FillQuantity: 0.1, FillPrice: 60000, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 100},
		{ClientOrderID: "nx-co-B", ExchangeOrderID: "nx-ex-B", FillQuantity: 0.2, FillPrice: 61000, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 200},
		{ClientOrderID: "nx-co-Z", ExchangeOrderID: "nx-ex-Z", FillQuantity: 0.9, FillPrice: 59000, FillFeeAsset: "USDT", FillFeeAmount: 0.4, FilledAtExchangeMs: 50},
	}
	for i := range fills {
		if err := repo.InsertSpotExecution(ctx, &fills[i]); err != nil {
			t.Fatalf("InsertSpotExecution %s: %v", fills[i].ExchangeOrderID, err)
		}
	}

	// sinceID=0 → both inst-1 fills, none from inst-2, ordered by id ASC.
	got, err := repo.NewExecutionsForInstance(ctx, "nx-inst-1", 0)
	if err != nil {
		t.Fatalf("NewExecutionsForInstance: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2 (inst-1 only); got %+v", len(got), got)
	}
	if got[0].ID >= got[1].ID {
		t.Errorf("not ordered by id ASC: %d then %d", got[0].ID, got[1].ID)
	}
	for _, f := range got {
		switch f.FillPrice {
		case 60000:
			if f.Side != "buy" {
				t.Errorf("fill@60000 Side = %q, want buy", f.Side)
			}
		case 61000:
			if f.Side != "sell" {
				t.Errorf("fill@61000 Side = %q, want sell", f.Side)
			}
		default:
			t.Errorf("unexpected fill price %v (inst-2 leaked?)", f.FillPrice)
		}
	}

	// Watermark: querying since the first row's id returns only the second.
	got2, err := repo.NewExecutionsForInstance(ctx, "nx-inst-1", got[0].ID)
	if err != nil {
		t.Fatalf("NewExecutionsForInstance(watermark): %v", err)
	}
	if len(got2) != 1 || got2[0].ID != got[1].ID {
		t.Fatalf("watermark filter wrong: got %+v, want only id %d", got2, got[1].ID)
	}
}

// TestTradeRepo_MaxExecutionIDForInstance proves the genesis watermark anchor:
// the max exec id for an instance, and 0 for an instance with no fills.
func TestTradeRepo_MaxExecutionIDForInstance(t *testing.T) {
	repo, ctx := openTradeTestDB(t)

	if err := repo.InsertTradeRecord(ctx, &store.TradeRecord{ClientOrderID: "nx-co-A", InstanceID: "nx-inst-1", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 1, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled}); err != nil {
		t.Fatalf("InsertTradeRecord: %v", err)
	}
	fills := []store.SpotExecution{
		{ClientOrderID: "nx-co-A", ExchangeOrderID: "nx-ex-1", FillQuantity: 0.1, FillPrice: 60000, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 100},
		{ClientOrderID: "nx-co-A", ExchangeOrderID: "nx-ex-2", FillQuantity: 0.1, FillPrice: 60000, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 200},
	}
	for i := range fills {
		if err := repo.InsertSpotExecution(ctx, &fills[i]); err != nil {
			t.Fatalf("InsertSpotExecution: %v", err)
		}
	}

	all, err := repo.NewExecutionsForInstance(ctx, "nx-inst-1", 0)
	if err != nil {
		t.Fatalf("NewExecutionsForInstance: %v", err)
	}
	wantMax := all[len(all)-1].ID

	maxID, err := repo.MaxExecutionIDForInstance(ctx, "nx-inst-1")
	if err != nil {
		t.Fatalf("MaxExecutionIDForInstance: %v", err)
	}
	if maxID != wantMax {
		t.Errorf("max id = %d, want %d", maxID, wantMax)
	}

	zero, err := repo.MaxExecutionIDForInstance(ctx, "nx-inst-none")
	if err != nil {
		t.Fatalf("MaxExecutionIDForInstance(empty): %v", err)
	}
	if zero != 0 {
		t.Errorf("max id for instance with no fills = %d, want 0", zero)
	}
}

// TestTradeRepo_MarkPartialIfPending proves the ① guard: a pending order
// advances to partial_filled; a non-pending order is left untouched.
func TestTradeRepo_MarkPartialIfPending(t *testing.T) {
	repo, ctx := openTradeTestDB(t)

	orders := []store.TradeRecord{
		{ClientOrderID: "nx-co-pending", InstanceID: "nx-inst-1", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 1, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusPending},
		{ClientOrderID: "nx-co-filled", InstanceID: "nx-inst-1", Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 1, NowMsAtSaaS: 1, ValidUntilMs: 2, Status: store.TradeStatusFilled},
	}
	for i := range orders {
		if err := repo.InsertTradeRecord(ctx, &orders[i]); err != nil {
			t.Fatalf("InsertTradeRecord %s: %v", orders[i].ClientOrderID, err)
		}
	}

	if err := repo.MarkPartialIfPending(ctx, "nx-co-pending"); err != nil {
		t.Fatalf("MarkPartialIfPending(pending): %v", err)
	}
	if err := repo.MarkPartialIfPending(ctx, "nx-co-filled"); err != nil {
		t.Fatalf("MarkPartialIfPending(filled): %v", err)
	}

	rows, err := repo.ListByInstance(ctx, "nx-inst-1", 0)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	got := map[string]store.TradeStatus{}
	for _, r := range rows {
		got[r.ClientOrderID] = r.Status
	}
	if got["nx-co-pending"] != store.TradeStatusPartialFilled {
		t.Errorf("pending order status = %q, want partial_filled", got["nx-co-pending"])
	}
	if got["nx-co-filled"] != store.TradeStatusFilled {
		t.Errorf("filled order status = %q, want filled (untouched)", got["nx-co-filled"])
	}
}

// TestTradeRepo_SweepOrphanPending proves the ④ orphan sweep cancels only
// the genuinely-orphaned pending rows: GTT lapsed AND never executed. The
// three negative controls (still in GTT window / pending-but-has-a-fill /
// already terminal) must be left untouched.
func TestTradeRepo_SweepOrphanPending(t *testing.T) {
	repo, ctx := openTradeTestDB(t)
	const inst = "nx-sweep-inst"
	const nowMs = 1000

	orders := []store.TradeRecord{
		// orphan: pending, GTT lapsed (500<1000), no fill → SWEPT.
		{ClientOrderID: "nx-sw-orphan", InstanceID: inst, Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1, ValidUntilMs: 500, Status: store.TradeStatusPending},
		// live: pending but still inside GTT window (2000>1000) → KEEP.
		{ClientOrderID: "nx-sw-live", InstanceID: inst, Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1, ValidUntilMs: 2000, Status: store.TradeStatusPending},
		// executed: pending + GTT lapsed but HAS a fill → KEEP (status-stuck, not orphan).
		{ClientOrderID: "nx-sw-executed", InstanceID: inst, Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1, ValidUntilMs: 500, Status: store.TradeStatusPending},
		// terminal: already filled → KEEP (not pending).
		{ClientOrderID: "nx-sw-filled", InstanceID: inst, Symbol: "BTCUSDT", Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1, ValidUntilMs: 500, Status: store.TradeStatusFilled},
	}
	for i := range orders {
		if err := repo.InsertTradeRecord(ctx, &orders[i]); err != nil {
			t.Fatalf("InsertTradeRecord %s: %v", orders[i].ClientOrderID, err)
		}
	}
	exec := &store.SpotExecution{ClientOrderID: "nx-sw-executed", ExchangeOrderID: "nx-sw-ex1", FillQuantity: 0.1, FillPrice: 60000, FillFeeAsset: "USDT", FillFeeAmount: 0.1, FilledAtExchangeMs: 400}
	if err := repo.InsertSpotExecution(ctx, exec); err != nil {
		t.Fatalf("InsertSpotExecution: %v", err)
	}

	n, err := repo.SweepOrphanPending(ctx, nowMs)
	if err != nil {
		t.Fatalf("SweepOrphanPending: %v", err)
	}
	if n != 1 {
		t.Errorf("swept %d rows, want 1 (only nx-sw-orphan)", n)
	}

	rows, err := repo.ListByInstance(ctx, inst, 0)
	if err != nil {
		t.Fatalf("ListByInstance: %v", err)
	}
	got := map[string]store.TradeStatus{}
	for _, r := range rows {
		got[r.ClientOrderID] = r.Status
	}
	if got["nx-sw-orphan"] != store.TradeStatusCancelled {
		t.Errorf("orphan status = %q, want cancelled", got["nx-sw-orphan"])
	}
	if got["nx-sw-live"] != store.TradeStatusPending {
		t.Errorf("in-window order status = %q, want pending (still valid)", got["nx-sw-live"])
	}
	if got["nx-sw-executed"] != store.TradeStatusPending {
		t.Errorf("executed-but-stuck order status = %q, want pending (has a fill, must not cancel)", got["nx-sw-executed"])
	}
	if got["nx-sw-filled"] != store.TradeStatusFilled {
		t.Errorf("filled order status = %q, want filled (untouched)", got["nx-sw-filled"])
	}

	// Idempotent: a second sweep finds nothing (the orphan is now cancelled).
	if n2, err := repo.SweepOrphanPending(ctx, nowMs); err != nil || n2 != 0 {
		t.Errorf("second sweep = (%d, %v), want (0, nil)", n2, err)
	}
}

// openDedupTestDB is the shared harness for the spot_executions dedup tests.
func openDedupTestDB(t *testing.T) (*TradeRepo, *gorm.DB, context.Context) {
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
	cleanup := func() {
		_ = db.Where("client_order_id LIKE ?", "dd-%").Delete(&store.SpotExecution{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)
	return NewTradeRepo(db), db, context.Background()
}

// TestTradeRepo_InsertSpotExecution_DedupByTradeID proves the uq_spot_exec_by_trade
// partial unique index: inserting the same (client_order_id, trade_id) twice
// results in exactly one row and no error (the second insert is a no-op).
func TestTradeRepo_InsertSpotExecution_DedupByTradeID(t *testing.T) {
	repo, db, ctx := openDedupTestDB(t)

	fill := store.SpotExecution{
		ClientOrderID:      "dd-co-1",
		ExchangeOrderID:    "dd-ex-1",
		FillQuantity:       0.1,
		FillPrice:          60000,
		FillFeeAsset:       "USDT",
		FillFeeAmount:      0.01,
		FilledAtExchangeMs: 1000,
		TradeID:            42,
	}

	if err := repo.InsertSpotExecution(ctx, &fill); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second insert with same (client_order_id, trade_id): must be a no-op.
	dup := fill
	dup.ID = 0 // reset GORM primary key so Create doesn't try an UPDATE
	if err := repo.InsertSpotExecution(ctx, &dup); err != nil {
		t.Fatalf("second insert (duplicate) returned error: %v", err)
	}

	var count int64
	if err := db.Model(&store.SpotExecution{}).
		Where("client_order_id = ? AND trade_id = ?", "dd-co-1", int64(42)).
		Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (duplicate must not insert a second row)", count)
	}
}

// TestTradeRepo_InsertSpotExecution_DedupByMs proves the uq_spot_exec_by_ms
// partial unique index (trade_id = 0 fallback path): inserting the same
// (client_order_id, filled_at_exchange_ms) twice when trade_id is absent
// results in exactly one row and no error.
func TestTradeRepo_InsertSpotExecution_DedupByMs(t *testing.T) {
	repo, db, ctx := openDedupTestDB(t)

	fill := store.SpotExecution{
		ClientOrderID:      "dd-co-2",
		ExchangeOrderID:    "dd-ex-2",
		FillQuantity:       0.2,
		FillPrice:          61000,
		FillFeeAsset:       "USDT",
		FillFeeAmount:      0.02,
		FilledAtExchangeMs: 2000,
		TradeID:            0, // no per-trade id (MockExchange)
	}

	if err := repo.InsertSpotExecution(ctx, &fill); err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second insert with same (client_order_id, filled_at_exchange_ms, trade_id=0).
	dup := fill
	dup.ID = 0
	if err := repo.InsertSpotExecution(ctx, &dup); err != nil {
		t.Fatalf("second insert (duplicate) returned error: %v", err)
	}

	var count int64
	if err := db.Model(&store.SpotExecution{}).
		Where("client_order_id = ? AND filled_at_exchange_ms = ? AND trade_id = 0", "dd-co-2", int64(2000)).
		Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Errorf("count = %d, want 1 (duplicate must not insert a second row)", count)
	}
}

// ---- UpdateTradeStatus monotonicity tests ----

// openMonotonicTestDB is the shared harness for UpdateTradeStatus monotonicity tests.
func openMonotonicTestDB(t *testing.T) (*TradeRepo, *gorm.DB, context.Context) {
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
	cleanup := func() {
		_ = db.Where("client_order_id LIKE ?", "mo-%").Delete(&store.TradeRecord{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)
	return NewTradeRepo(db), db, context.Background()
}

func insertMoOrder(t *testing.T, repo *TradeRepo, ctx context.Context, id string, status store.TradeStatus) {
	t.Helper()
	if err := repo.InsertTradeRecord(ctx, &store.TradeRecord{
		ClientOrderID: id,
		InstanceID:    "mo-inst",
		Symbol:        "BTCUSDT",
		Side:          "buy",
		OrderType:     "market",
		QuantityUSD:   100,
		NowMsAtSaaS:   1,
		ValidUntilMs:  9999999999,
		Status:        status,
	}); err != nil {
		t.Fatalf("InsertTradeRecord %s: %v", id, err)
	}
}

func queryStatus(t *testing.T, db *gorm.DB, ctx context.Context, id string) store.TradeStatus {
	t.Helper()
	var tr store.TradeRecord
	if err := db.WithContext(ctx).Where("client_order_id = ?", id).First(&tr).Error; err != nil {
		t.Fatalf("fetch TradeRecord %s: %v", id, err)
	}
	return tr.Status
}

// TestTradeRepo_UpdateTradeStatus_FilledNoDowngrade proves that a filled
// TradeRecord is not downgraded when partial_filled or rejected arrives
// (e.g. a delayed order_update or ack replay).
func TestTradeRepo_UpdateTradeStatus_FilledNoDowngrade(t *testing.T) {
	repo, db, ctx := openMonotonicTestDB(t)

	insertMoOrder(t, repo, ctx, "mo-filled-1", store.TradeStatusFilled)

	if err := repo.UpdateTradeStatus(ctx, "mo-filled-1", store.TradeStatusPartialFilled); err != nil {
		t.Fatalf("UpdateTradeStatus(partial_filled on filled): unexpected error: %v", err)
	}
	if got := queryStatus(t, db, ctx, "mo-filled-1"); got != store.TradeStatusFilled {
		t.Errorf("status = %q after partial_filled update, want filled (must not downgrade)", got)
	}

	if err := repo.UpdateTradeStatus(ctx, "mo-filled-1", store.TradeStatusRejected); err != nil {
		t.Fatalf("UpdateTradeStatus(rejected on filled): unexpected error: %v", err)
	}
	if got := queryStatus(t, db, ctx, "mo-filled-1"); got != store.TradeStatusFilled {
		t.Errorf("status = %q after rejected update, want filled (must not downgrade)", got)
	}
}

// TestTradeRepo_UpdateTradeStatus_ExpiredAckNoCancel proves that an expired
// ack (mapped to cancelled by ackToTradeStatus) cannot overwrite a filled row.
func TestTradeRepo_UpdateTradeStatus_ExpiredAckNoCancel(t *testing.T) {
	repo, db, ctx := openMonotonicTestDB(t)

	insertMoOrder(t, repo, ctx, "mo-filled-2", store.TradeStatusFilled)

	if err := repo.UpdateTradeStatus(ctx, "mo-filled-2", store.TradeStatusCancelled); err != nil {
		t.Fatalf("UpdateTradeStatus(cancelled on filled): unexpected error: %v", err)
	}
	if got := queryStatus(t, db, ctx, "mo-filled-2"); got != store.TradeStatusFilled {
		t.Errorf("status = %q after cancelled update, want filled (expired ack must not cancel)", got)
	}
}

// TestTradeRepo_UpdateTradeStatus_NonTerminalAdvances proves the normal
// forward transitions still work: pending → partial_filled → filled.
func TestTradeRepo_UpdateTradeStatus_NonTerminalAdvances(t *testing.T) {
	repo, db, ctx := openMonotonicTestDB(t)

	insertMoOrder(t, repo, ctx, "mo-adv-1", store.TradeStatusPending)

	if err := repo.UpdateTradeStatus(ctx, "mo-adv-1", store.TradeStatusPartialFilled); err != nil {
		t.Fatalf("pending→partial_filled: %v", err)
	}
	if got := queryStatus(t, db, ctx, "mo-adv-1"); got != store.TradeStatusPartialFilled {
		t.Errorf("status = %q, want partial_filled", got)
	}

	if err := repo.UpdateTradeStatus(ctx, "mo-adv-1", store.TradeStatusFilled); err != nil {
		t.Fatalf("partial_filled→filled: %v", err)
	}
	if got := queryStatus(t, db, ctx, "mo-adv-1"); got != store.TradeStatusFilled {
		t.Errorf("status = %q, want filled", got)
	}
}
