// agentmsg.go — OnAgentMessage hook for wshub.Hub. Decodes Ack /
// OrderUpdate envelopes and writes them to TradeRecord + SpotExecution
// via TradeRepo. The hook fires after wshub has already validated the
// envelope, so we can decode the typed payload directly.
//
// DeltaReport (reconciliation snapshot) is logged but not yet persisted:
// the reconciliation logic that turns it into TradeRecord/SpotLot
// discrepancies is a separate Phase 8 work item.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

	"gorm.io/gorm"

	"quantlab/internal/repository"
	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

// agentMessageHandler holds the dependencies the OnAgentMessage closure
// captures. Constructed in main.go; the Hook method is what gets stored
// in wshub.Config.OnAgentMessage.
type agentMessageHandler struct {
	trades *repository.TradeRepo
	logger *slog.Logger
}

func newAgentMessageHandler(trades *repository.TradeRepo, logger *slog.Logger) *agentMessageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &agentMessageHandler{trades: trades, logger: logger}
}

// Hook implements the wshub.Config.OnAgentMessage signature. Errors
// returned here are surfaced to wshub's structured log line but do NOT
// tear down the connection — wire-level errors (bad payload) and
// DB-level errors (transient gorm failures) are both "best-effort
// persistence" from the Hub's point of view; the Agent has already
// committed the work locally.
func (h *agentMessageHandler) Hook(ctx context.Context, accountID string, env wire.Envelope) error {
	switch env.Type {
	case wire.TypeAck:
		ack, err := wire.DecodePayload[wire.Ack](env)
		if err != nil {
			return fmt.Errorf("agentmsg: decode ack: %w", err)
		}
		return h.handleAck(ctx, accountID, ack)

	case wire.TypeOrderUpdate:
		ou, err := wire.DecodePayload[wire.OrderUpdate](env)
		if err != nil {
			return fmt.Errorf("agentmsg: decode order_update: %w", err)
		}
		return h.handleOrderUpdate(ctx, accountID, ou)

	case wire.TypeDeltaReport:
		// Reconciliation snapshot. Phase 8 polish wires this into
		// position diff detection.
		h.logger.Info("delta_report_received", "account_id", accountID,
			"msg_id", env.MsgID)
		return nil
	}
	return nil
}

func (h *agentMessageHandler) handleAck(ctx context.Context, accountID string, ack *wire.Ack) error {
	status, persist := ackToTradeStatus(ack.Status)
	if !persist {
		h.logger.Debug("ack_status_noop",
			"account_id", accountID,
			"client_order_id", ack.ClientOrderID,
			"ack_status", ack.Status)
		return nil
	}
	if err := h.trades.UpdateTradeStatus(ctx, ack.ClientOrderID, status); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Warn("ack_for_unknown_order",
				"account_id", accountID,
				"client_order_id", ack.ClientOrderID)
			return nil
		}
		return err
	}
	return nil
}

func (h *agentMessageHandler) handleOrderUpdate(ctx context.Context, accountID string, ou *wire.OrderUpdate) error {
	// Persist fills first so even a status update failure leaves the
	// execution rows in place for auditing.
	for i, f := range ou.Fills {
		ex, err := buildSpotExecution(ou, f)
		if err != nil {
			return fmt.Errorf("agentmsg: order_update fill[%d]: %w", i, err)
		}
		if err := h.trades.InsertSpotExecution(ctx, ex); err != nil {
			return err
		}
	}

	status, ok := orderUpdateToTradeStatus(ou.Status)
	if !ok {
		return fmt.Errorf("agentmsg: order_update unknown status %q", ou.Status)
	}
	if err := h.trades.UpdateTradeStatus(ctx, ou.ClientOrderID, status); err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			h.logger.Warn("order_update_for_unknown_order",
				"account_id", accountID,
				"client_order_id", ou.ClientOrderID)
			return nil
		}
		return err
	}
	return nil
}

// ackToTradeStatus maps wire.AckStatus → store.TradeStatus.
//
// The second return is false for ack statuses that should not move
// the TradeRecord (accepted → still pending awaiting fill;
// duplicate_pending → another active order owns this row).
func ackToTradeStatus(s wire.AckStatus) (store.TradeStatus, bool) {
	switch s {
	case wire.AckStatusRejected:
		return store.TradeStatusRejected, true
	case wire.AckStatusExpired:
		// "expired" is the Agent's way of saying the command's
		// valid_until_ms had already passed; treat as cancelled so
		// downstream lot logic doesn't expect a fill.
		return store.TradeStatusCancelled, true
	case wire.AckStatusDuplicateTerminal:
		// A prior copy of this command already completed; the row
		// should already be in a terminal state. We don't overwrite.
		return "", false
	case wire.AckStatusAccepted, wire.AckStatusDuplicatePending:
		// Stay pending until OrderUpdate arrives.
		return "", false
	}
	return "", false
}

func orderUpdateToTradeStatus(s wire.OrderStatus) (store.TradeStatus, bool) {
	switch s {
	case wire.OrderStatusFilled:
		return store.TradeStatusFilled, true
	case wire.OrderStatusPartialFilled:
		return store.TradeStatusPartialFilled, true
	case wire.OrderStatusCancelled:
		return store.TradeStatusCancelled, true
	case wire.OrderStatusRejected:
		return store.TradeStatusRejected, true
	}
	return "", false
}

// buildSpotExecution turns a wire.Fill (decimal strings) into a
// store.SpotExecution (float64 in SI precision). The decimal→float
// cast is lossy but acceptable per docs/saas-ws-protocol-v1.md §2.2
// "numeric exception": SaaS-side bookkeeping uses float64, the wire
// uses strings only to avoid JSON float rounding.
func buildSpotExecution(ou *wire.OrderUpdate, f wire.Fill) (*store.SpotExecution, error) {
	qty, err := strconv.ParseFloat(f.FillQuantityDecimal, 64)
	if err != nil {
		return nil, fmt.Errorf("fill_quantity_decimal=%q: %w", f.FillQuantityDecimal, err)
	}
	price, err := strconv.ParseFloat(f.FillPriceDecimal, 64)
	if err != nil {
		return nil, fmt.Errorf("fill_price_decimal=%q: %w", f.FillPriceDecimal, err)
	}
	fee, err := strconv.ParseFloat(f.FillFeeAmountDecimal, 64)
	if err != nil {
		return nil, fmt.Errorf("fill_fee_amount_decimal=%q: %w", f.FillFeeAmountDecimal, err)
	}
	return &store.SpotExecution{
		ClientOrderID:      ou.ClientOrderID,
		ExchangeOrderID:    ou.ExchangeOrderID,
		FillQuantity:       qty,
		FillPrice:          price,
		FillFeeAsset:       f.FillFeeAsset,
		FillFeeAmount:      fee,
		FilledAtExchangeMs: f.FilledAtExchangeMs,
		ActualSlippageBPS:  f.ActualSlippageBps,
	}, nil
}
