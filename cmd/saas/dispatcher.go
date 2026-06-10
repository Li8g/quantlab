// dispatcher.go — recordingDispatcher wraps wshub.Hub with a pre-insert
// step so SaaS has a pending TradeRecord row for every OrderIntent it
// asks the Agent to execute. The Agent's later Ack / OrderUpdate frames
// update that row via the OnAgentMessage hook (agentmsg.go).
//
// Failure semantics: if the DB insert fails, we do NOT call hub.Dispatch
// — without a TradeRecord the Ack would orphan. The Tick will surface
// the error to the caller (cron) which retries on the next interval.
// Duplicate-key inserts (gorm.ErrDuplicatedKey) are treated as
// "already recorded" so a redispatch (caller retry after a transient
// WS failure) does not double-account.
package main

import (
	"context"
	"errors"
	"log/slog"

	"gorm.io/gorm"

	"quantlab/internal/repository"
	"quantlab/internal/saas/instance"
	"quantlab/internal/saas/store"
	"quantlab/internal/saas/wshub"
	"quantlab/internal/strategy"
)

type recordingDispatcher struct {
	inner       instance.TradeCommandDispatcher
	trades      *repository.TradeRepo
	priceCapBps float64
	logger      *slog.Logger
}

// priceCapBps is the same B2 cap the inner Hub dispatches with (sourced from
// cfg.Orders in main), so the pre-insert TradeRecord records the marketable
// LIMIT IOC actually sent, not the raw market intent.
func newRecordingDispatcher(inner instance.TradeCommandDispatcher, trades *repository.TradeRepo, priceCapBps float64, logger *slog.Logger) *recordingDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &recordingDispatcher{inner: inner, trades: trades, priceCapBps: priceCapBps, logger: logger}
}

func (d *recordingDispatcher) Dispatch(ctx context.Context, instanceID, accountID, symbol string, latestClose float64, orders []strategy.OrderIntent) error {
	for _, oi := range orders {
		tr, err := buildTradeRecord(instanceID, symbol, oi, latestClose, d.priceCapBps)
		if err != nil {
			return err
		}
		if err := d.trades.InsertTradeRecord(ctx, tr); err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				d.logger.Info("trade_record_duplicate_ignored",
					"client_order_id", oi.ClientOrderID,
					"instance_id", instanceID)
				continue
			}
			return err
		}
	}
	return d.inner.Dispatch(ctx, instanceID, accountID, symbol, latestClose, orders)
}

// buildTradeRecord maps OrderIntent → store.TradeRecord, recording the order
// as actually dispatched after B2 price-cap conversion (a market intent under
// a positive cap becomes a marketable LIMIT IOC at latestClose×(1±cap)) via
// the same wshub.EffectiveDispatchedOrder the wire frame uses — so the ledger
// row matches the exchange order the Agent's Ack/OrderUpdate update it with.
//
// now_ms_at_saas is left at 0 because it is also written into the wire frame
// by wshub.buildTradeCommand (already the source of truth for the dispatched
// timestamp); SaaS doesn't need a second copy at this stage.
func buildTradeRecord(instanceID, symbol string, oi strategy.OrderIntent, latestClose, priceCapBps float64) (*store.TradeRecord, error) {
	eff, err := wshub.EffectiveDispatchedOrder(oi, latestClose, priceCapBps)
	if err != nil {
		return nil, err
	}
	tr := &store.TradeRecord{
		ClientOrderID: oi.ClientOrderID,
		InstanceID:    instanceID,
		Symbol:        symbol,
		Side:          string(oi.Side),
		OrderType:     string(eff.OrderType),
		QuantityUSD:   oi.QuantityUSD,
		ValidUntilMs:  oi.ValidUntilMs,
		Status:        store.TradeStatusPending,
	}
	if eff.OrderType == strategy.OrderTypeLimit {
		lp := eff.LimitPrice
		tr.LimitPrice = &lp
	}
	return tr, nil
}
