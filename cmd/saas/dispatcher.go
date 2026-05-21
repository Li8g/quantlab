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
	"quantlab/internal/strategy"
)

type recordingDispatcher struct {
	inner  instance.TradeCommandDispatcher
	trades *repository.TradeRepo
	logger *slog.Logger
}

func newRecordingDispatcher(inner instance.TradeCommandDispatcher, trades *repository.TradeRepo, logger *slog.Logger) *recordingDispatcher {
	if logger == nil {
		logger = slog.Default()
	}
	return &recordingDispatcher{inner: inner, trades: trades, logger: logger}
}

func (d *recordingDispatcher) Dispatch(ctx context.Context, instanceID, accountID, symbol string, latestClose float64, orders []strategy.OrderIntent) error {
	for _, oi := range orders {
		tr := buildTradeRecord(instanceID, symbol, oi)
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

// buildTradeRecord maps OrderIntent → store.TradeRecord. now_ms_at_saas
// is left at 0 because it is also written into the wire frame by
// wshub.buildTradeCommand (already the source of truth for the
// dispatched timestamp); SaaS doesn't need a second copy at this stage.
// Phase 8 polish can stamp it here for end-to-end latency analysis.
func buildTradeRecord(instanceID, symbol string, oi strategy.OrderIntent) *store.TradeRecord {
	tr := &store.TradeRecord{
		ClientOrderID: oi.ClientOrderID,
		InstanceID:    instanceID,
		Symbol:        symbol,
		Side:          string(oi.Side),
		OrderType:     string(oi.OrderType),
		QuantityUSD:   oi.QuantityUSD,
		ValidUntilMs:  oi.ValidUntilMs,
		Status:        store.TradeStatusPending,
	}
	if oi.OrderType == strategy.OrderTypeLimit {
		lp := oi.LimitPrice
		tr.LimitPrice = &lp
	}
	return tr
}
