//go:build integration

// Integration test for the fill-dedup chokepoint (②) against a live Postgres
// instance. The bug: the exchange event stream is at-least-once (Binance WS
// API replays execution reports on resubscribe/reconnect) and each replay
// rides a fresh envelope msg_id, so an order_update fill that inserted a
// SpotExecution unconditionally would insert a *second* row on replay. Its
// fresh auto-increment ID then looks like a brand-new fill to the ③ ledger
// fold, double-counting the position into a drift that auto-freezes the agent.
// agentmsg.insertFillIfNew now dedups both fill channels on the content key
// (client_order_id, filled_at_exchange_ms); these tests pin that exactly one
// SpotExecution row survives a replay and a cross-channel duplicate. Run:
//
//	go test -tags=integration ./cmd/saas/ -args -config=/absolute/path/to/config.yaml
package main

import (
	"context"
	"testing"

	"quantlab/internal/repository"
	"quantlab/internal/saas/config"
	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

const dedupAccount = "dedup-test-acct"

// TestOrderUpdateFillDedup_Integration replays an identical order_update (the
// at-least-once stream redelivery) and pins that the fill lands exactly once,
// then pins the cross-channel case: a delta_report carrying the same fill adds
// no second row either.
func TestOrderUpdateFillDedup_Integration(t *testing.T) {
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

	trades := repository.NewTradeRepo(db)
	instances := repository.NewInstanceRepo(db)
	portfolios := repository.NewPortfolioRepo(db)
	recon := repository.NewReconRepo(db)

	const clientOrderID = "dedup-co-1"
	cleanup := func() {
		_ = db.Where("client_order_id = ?", clientOrderID).Delete(&store.SpotExecution{}).Error
		_ = db.Where("client_order_id = ?", clientOrderID).Delete(&store.TradeRecord{}).Error
	}
	cleanup()
	t.Cleanup(cleanup)

	// The order exists in pending state so we can also assert the status flip.
	if err := trades.InsertTradeRecord(ctx, &store.TradeRecord{
		ClientOrderID: clientOrderID, InstanceID: "dedup-inst-1", Symbol: "BTCUSDT",
		Side: "buy", OrderType: "market", QuantityUSD: 100, NowMsAtSaaS: 1,
		ValidUntilMs: 2, Status: store.TradeStatusPending,
	}); err != nil {
		t.Fatalf("InsertTradeRecord: %v", err)
	}

	h := newAgentMessageHandler(trades, instances, portfolios, recon, nil)

	countRows := func() int64 {
		var n int64
		db.Model(&store.SpotExecution{}).Where("client_order_id = ?", clientOrderID).Count(&n)
		return n
	}

	ou := &wire.OrderUpdate{
		ClientOrderID:   clientOrderID,
		ExchangeOrderID: "dedup-ex-1",
		Status:          wire.OrderStatusFilled,
		Fills: []wire.Fill{{
			FillQuantityDecimal:  "0.1",
			FillPriceDecimal:     "60000",
			FillFeeAsset:         "USDT",
			FillFeeAmountDecimal: "0.6",
			FilledAtExchangeMs:   1714000000000,
			ActualSlippageBps:    1.0,
			TradeID:              5001,
		}},
	}

	// First delivery inserts the fill.
	if err := h.handleOrderUpdate(ctx, dedupAccount, ou); err != nil {
		t.Fatalf("handleOrderUpdate #1: %v", err)
	}
	if n := countRows(); n != 1 {
		t.Fatalf("after first order_update: %d rows, want 1", n)
	}

	// Stream replay: identical frame redelivered (same trade_id). No 2nd row.
	if err := h.handleOrderUpdate(ctx, dedupAccount, ou); err != nil {
		t.Fatalf("handleOrderUpdate #2 (replay): %v", err)
	}
	if n := countRows(); n != 1 {
		t.Fatalf("after replayed order_update: %d rows, want 1 (② double-insert)", n)
	}

	// Cross-channel: the same trade arrives on the delta_report fallback (it is
	// tee'd into both channels at the agent). Same trade_id ⇒ no second row.
	dr := &wire.DeltaReport{ReportedAtMs: 1714000001000}
	dr.SinceLastReport.Fills = []wire.Fill{{
		FillQuantityDecimal:  "0.1",
		FillPriceDecimal:     "60000",
		FillFeeAsset:         "USDT",
		FillFeeAmountDecimal: "0.6",
		FilledAtExchangeMs:   1714000000000,
		ActualSlippageBps:    1.0,
		TradeID:              5001,
		ClientOrderID:        clientOrderID,
		ExchangeOrderID:      "dedup-ex-1",
	}}
	if err := h.recoverFills(ctx, dedupAccount, dr); err != nil {
		t.Fatalf("recoverFills (cross-channel): %v", err)
	}
	if n := countRows(); n != 1 {
		t.Fatalf("after cross-channel delta_report: %d rows, want 1", n)
	}

	// ②.5 regression: a market order sweeping a thin book yields several
	// genuine fills that all SHARE filled_at_exchange_ms but have DISTINCT
	// trade_ids. They must each land — an ms-only key would collapse them into
	// one and under-count the position into an auto-freeze.
	sweep := &wire.OrderUpdate{
		ClientOrderID:   clientOrderID,
		ExchangeOrderID: "dedup-ex-1",
		Status:          wire.OrderStatusFilled,
		Fills: []wire.Fill{
			{FillQuantityDecimal: "0.001", FillPriceDecimal: "60010", FillFeeAsset: "USDT", FillFeeAmountDecimal: "0", FilledAtExchangeMs: 1714000002000, TradeID: 6001},
			{FillQuantityDecimal: "0.002", FillPriceDecimal: "60005", FillFeeAsset: "USDT", FillFeeAmountDecimal: "0", FilledAtExchangeMs: 1714000002000, TradeID: 6002},
			{FillQuantityDecimal: "0.003", FillPriceDecimal: "60000", FillFeeAsset: "USDT", FillFeeAmountDecimal: "0", FilledAtExchangeMs: 1714000002000, TradeID: 6003},
		},
	}
	if err := h.handleOrderUpdate(ctx, dedupAccount, sweep); err != nil {
		t.Fatalf("handleOrderUpdate (sweep): %v", err)
	}
	if n := countRows(); n != 4 {
		t.Fatalf("after same-ms sweep: %d rows, want 4 (1 + 3 distinct trade_ids; ms key must NOT collapse)", n)
	}
	// Replaying the whole sweep adds nothing (each trade_id already stored).
	if err := h.handleOrderUpdate(ctx, dedupAccount, sweep); err != nil {
		t.Fatalf("handleOrderUpdate (sweep replay): %v", err)
	}
	if n := countRows(); n != 4 {
		t.Fatalf("after sweep replay: %d rows, want 4 (trade_id dedup)", n)
	}
}
