// agentmsg.go — OnAgentMessage hook for wshub.Hub. Decodes Ack /
// OrderUpdate / DeltaReport envelopes and persists them. The hook fires
// after wshub has already validated the envelope, so we can decode the
// typed payload directly.
//
// DeltaReport (low-freq reconciliation snapshot, §5.11) is the Phase 8
// 持仓对账 channel: fallback fills are recovered (deduped) into
// SpotExecution, exchange-truth positions are diffed against SaaS-side
// portfolio bookkeeping into ReconciliationDiscrepancy, and exchange-layer
// errors are persisted as AgentError. All three are durable forensic
// tables (Option 2) rather than logs, so incident retrospective survives
// log rotation.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"quantlab/internal/repository"
	"quantlab/internal/saas/store"
	"quantlab/internal/wire"
)

// agentMessageHandler holds the dependencies the OnAgentMessage closure
// captures. Constructed in main.go; the Hook method is what gets stored
// in wshub.Config.OnAgentMessage.
type agentMessageHandler struct {
	trades     *repository.TradeRepo
	instances  *repository.InstanceRepo
	portfolios *repository.PortfolioRepo
	recon      *repository.ReconRepo
	logger     *slog.Logger

	// killer delivers the auto-freeze kill_switch (Option 3). nil ⇒
	// auto-freeze disabled (drift is still recorded); main.go injects the
	// wshub.Hub via SetKillSwitchSender after the hub is constructed
	// (the hub needs Hook, Hook needs the hub — broken by post-construction
	// injection).
	killer killSwitchSender
	// freezeMu guards driftStreak across concurrent per-account
	// delta_report handling. driftStreak[accountID] is the consecutive
	// over-the-freeze-line report count; killedSentinel suppresses repeats.
	freezeMu    sync.Mutex
	driftStreak map[string]int
	// auditor records the instance.kill action trail (Option 3 step 5).
	// nil ⇒ no audit row (drift is still acted on). Set in main.go.
	auditor auditSink
}

// killSwitchSender is the narrow control-plane dependency for auto-freeze
// (satisfied by *wshub.Hub). Kept as an interface so agentmsg has no
// compile dependency on wshub and the trigger logic stays unit-testable.
type killSwitchSender interface {
	SendKillSwitch(accountID string, ks wire.KillSwitch) error
}

// SetKillSwitchSender wires the auto-freeze sender post-construction
// (see killer field). Safe to leave unset — auto-freeze becomes a no-op.
func (h *agentMessageHandler) SetKillSwitchSender(k killSwitchSender) { h.killer = k }

func newAgentMessageHandler(
	trades *repository.TradeRepo,
	instances *repository.InstanceRepo,
	portfolios *repository.PortfolioRepo,
	recon *repository.ReconRepo,
	logger *slog.Logger,
) *agentMessageHandler {
	if logger == nil {
		logger = slog.Default()
	}
	return &agentMessageHandler{
		trades:      trades,
		instances:   instances,
		portfolios:  portfolios,
		recon:       recon,
		logger:      logger,
		driftStreak: make(map[string]int),
	}
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
		dr, err := wire.DecodePayload[wire.DeltaReport](env)
		if err != nil {
			return fmt.Errorf("agentmsg: decode delta_report: %w", err)
		}
		return h.handleDeltaReport(ctx, accountID, dr)
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
	// order_update fills carry no client/exchange order id of their own —
	// the enclosing OrderUpdate names the order (§5.10).
	return buildSpotExecutionFrom(ou.ClientOrderID, ou.ExchangeOrderID, f)
}

// buildSpotExecutionFrom is the order-id-explicit variant. delta_report
// fills (§5.11) carry their own ClientOrderID/ExchangeOrderID, so the
// reconciliation path passes them directly rather than from a parent
// OrderUpdate.
func buildSpotExecutionFrom(clientOrderID, exchangeOrderID string, f wire.Fill) (*store.SpotExecution, error) {
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
		ClientOrderID:      clientOrderID,
		ExchangeOrderID:    exchangeOrderID,
		FillQuantity:       qty,
		FillPrice:          price,
		FillFeeAsset:       f.FillFeeAsset,
		FillFeeAmount:      fee,
		FilledAtExchangeMs: f.FilledAtExchangeMs,
		ActualSlippageBPS:  f.ActualSlippageBps,
	}, nil
}

// reconcileToleranceBps — relative drift beyond this (and beyond the
// per-asset dust floor) is recorded as a discrepancy. [INVENTED v1:
// 0.5% accommodates fee/rounding noise while still catching a missed
// fill or out-of-band manual intervention; tune as real data arrives.]
const reconcileToleranceBps = 50.0

// minAbsDiff gates dust: a drift only counts if the absolute difference
// also clears this per-asset floor, so a large *relative* drift near zero
// (e.g. expected 0, actual 1e-9) doesn't fire. [INVENTED v1]
func minAbsDiff(asset string) float64 {
	if asset == "USDT" {
		return 0.01
	}
	return 1e-6 // BTC / other base assets
}

// driftResult is one asset's computed drift before persistence. Flagged
// marks the ones that breach both gates (relative tolerance + dust floor).
type driftResult struct {
	Asset    string
	Expected float64 // SaaS bookkeeping
	Actual   float64 // exchange free+locked
	Diff     float64 // actual - expected
	DriftBps float64
	Flagged  bool
}

// reconcilePositions diffs SaaS-side expected holdings (per asset)
// against the exchange-truth positions from a delta_report. Pure — no
// DB, no clock — so the 对账 math is unit-testable. Reconciles the union
// of assets on either side: an exchange asset SaaS doesn't track (or vice
// versa) surfaces as drift against a zero baseline. [INVENTED v1:
// free+locked summation, tolerance, dust floor]
func reconcilePositions(expected map[string]float64, positions []wire.Position) ([]driftResult, error) {
	actual := make(map[string]float64, len(positions))
	for _, p := range positions {
		free, err := strconv.ParseFloat(p.FreeDecimal, 64)
		if err != nil {
			return nil, fmt.Errorf("position %s free_decimal=%q: %w", p.Symbol, p.FreeDecimal, err)
		}
		locked, err := strconv.ParseFloat(p.LockedDecimal, 64)
		if err != nil {
			return nil, fmt.Errorf("position %s locked_decimal=%q: %w", p.Symbol, p.LockedDecimal, err)
		}
		actual[p.Symbol] = free + locked
	}

	assetSet := make(map[string]struct{}, len(expected)+len(actual))
	for a := range expected {
		assetSet[a] = struct{}{}
	}
	for a := range actual {
		assetSet[a] = struct{}{}
	}
	keys := make([]string, 0, len(assetSet))
	for a := range assetSet {
		keys = append(keys, a)
	}
	sort.Strings(keys) // deterministic order (persistence + tests)

	out := make([]driftResult, 0, len(keys))
	for _, a := range keys {
		exp, act := expected[a], actual[a]
		diff := act - exp
		denom := math.Max(math.Max(math.Abs(exp), math.Abs(act)), 1e-9)
		driftBps := math.Abs(diff) / denom * 1e4
		out = append(out, driftResult{
			Asset:    a,
			Expected: exp,
			Actual:   act,
			Diff:     diff,
			DriftBps: driftBps,
			Flagged:  math.Abs(diff) > minAbsDiff(a) && driftBps > reconcileToleranceBps,
		})
	}
	return out, nil
}

// handleDeltaReport persists the three parts of a delta_report (§5.11):
// fallback fills (deduped), position drift, and exchange-layer errors.
// Each part is independent — a failure in one still attempts the rest is
// NOT done here (we return on first DB error, matching the other handlers'
// best-effort-but-surface-errors contract); the Agent has already
// committed the work locally, so a transient DB error just means this
// snapshot is retried-or-lost, not that state diverges.
func (h *agentMessageHandler) handleDeltaReport(ctx context.Context, accountID string, dr *wire.DeltaReport) error {
	// Resolve account → instance(s). 1:1 lets us attribute rows to an
	// instance (Tier L per-instance views); a fan-out leaves InstanceID
	// empty (account-level). [INVENTED v1]
	insts, err := h.instances.ListByAccount(ctx, accountID)
	if err != nil {
		return fmt.Errorf("agentmsg: delta_report list instances: %w", err)
	}
	instanceID := ""
	if len(insts) == 1 {
		instanceID = insts[0].InstanceID
	}

	// 1. Fallback fills → SpotExecution, deduped against order_update.
	if err := h.recoverFills(ctx, accountID, dr); err != nil {
		return err
	}

	// 2. Position reconciliation → ReconciliationDiscrepancy.
	if err := h.reconcile(ctx, accountID, instanceID, insts, dr); err != nil {
		return err
	}

	// 3. Exchange-layer errors → AgentError.
	for _, e := range dr.SinceLastReport.Errors {
		row := &store.AgentError{
			AccountID:    accountID,
			InstanceID:   instanceID,
			Code:         e.Code,
			Message:      e.Message,
			OccurredAtMs: e.OccurredAtMs,
			ReportedAtMs: dr.ReportedAtMs,
		}
		if err := h.recon.InsertAgentError(ctx, row); err != nil {
			return fmt.Errorf("agentmsg: delta_report insert agent_error: %w", err)
		}
	}
	return nil
}

// recoverFills persists delta_report fills that order_update never
// delivered. delta_report is the loss-tolerant fallback channel; status
// convergence stays with order_update/ack (a delta_report fill carries no
// order status), so this recovers the SpotExecution audit row only and
// leaves TradeRecord.Status untouched. [INVENTED v1]
func (h *agentMessageHandler) recoverFills(ctx context.Context, accountID string, dr *wire.DeltaReport) error {
	for i, f := range dr.SinceLastReport.Fills {
		if f.ClientOrderID == "" {
			h.logger.Warn("delta_report_fill_missing_client_order_id",
				"account_id", accountID, "filled_at_exchange_ms", f.FilledAtExchangeMs)
			continue
		}
		exists, err := h.trades.ExecutionExists(ctx, f.ClientOrderID, f.FilledAtExchangeMs)
		if err != nil {
			return fmt.Errorf("agentmsg: delta_report dedup check: %w", err)
		}
		if exists {
			continue // already persisted via order_update — skip
		}
		ex, err := buildSpotExecutionFrom(f.ClientOrderID, f.ExchangeOrderID, f)
		if err != nil {
			return fmt.Errorf("agentmsg: delta_report fill[%d]: %w", i, err)
		}
		if err := h.trades.InsertSpotExecution(ctx, ex); err != nil {
			return fmt.Errorf("agentmsg: delta_report insert recovered fill: %w", err)
		}
		h.logger.Warn("delta_report_recovered_fill",
			"account_id", accountID, "client_order_id", f.ClientOrderID,
			"filled_at_exchange_ms", f.FilledAtExchangeMs)
	}
	return nil
}

// reconcile aggregates the SaaS-side expected holdings across the
// account's instances' latest portfolio snapshots, diffs them against the
// exchange-truth positions, and persists any flagged drift. Skips quietly
// when there's no baseline yet (cold start, never ticked) so a fresh
// instance doesn't false-positive its real exchange balance as drift.
func (h *agentMessageHandler) reconcile(
	ctx context.Context, accountID, instanceID string,
	insts []store.StrategyInstance, dr *wire.DeltaReport,
) error {
	expected := map[string]float64{}
	hasBaseline := false
	for i := range insts {
		ps, err := h.portfolios.Latest(ctx, insts[i].InstanceID)
		if err != nil {
			return fmt.Errorf("agentmsg: delta_report portfolio latest: %w", err)
		}
		if ps == nil {
			continue // instance never ticked — no baseline contribution
		}
		hasBaseline = true
		base := strings.TrimSuffix(insts[i].Pair, "USDT") // [INVENTED v1] base asset from pair
		expected[base] += ps.DeadBTC + ps.FloatBTC + ps.ColdSealedBTC
		expected["USDT"] += ps.USDT
	}
	if !hasBaseline {
		h.logger.Info("delta_report_reconcile_skipped_no_baseline", "account_id", accountID)
		return nil
	}

	drifts, err := reconcilePositions(expected, dr.Positions)
	if err != nil {
		return fmt.Errorf("agentmsg: delta_report reconcile: %w", err)
	}
	now := time.Now().UnixMilli()
	for _, d := range drifts {
		if !d.Flagged {
			continue
		}
		h.logger.Warn("delta_report_discrepancy",
			"account_id", accountID, "asset", d.Asset,
			"expected", d.Expected, "actual", d.Actual, "drift_bps", d.DriftBps)
		row := &store.ReconciliationDiscrepancy{
			AccountID:      accountID,
			InstanceID:     instanceID,
			Asset:          d.Asset,
			ExpectedAmount: d.Expected,
			ActualAmount:   d.Actual,
			DiffAmount:     d.Diff,
			DriftBps:       d.DriftBps,
			ReportedAtMs:   dr.ReportedAtMs,
			DetectedAtMs:   now,
		}
		if err := h.recon.InsertDiscrepancy(ctx, row); err != nil {
			return fmt.Errorf("agentmsg: delta_report insert discrepancy: %w", err)
		}
	}

	// Auto-freeze (kill_switch Option 3): a sustained drift trips the kill,
	// but only on the MANAGED assets — expected's keys, i.e. the account's
	// instances' base assets + USDT. Unmanaged exchange balances (e.g. a
	// testnet faucet's unrelated coins) are recorded above as discrepancies
	// for the forensic trail but must not auto-halt a live agent.
	managed := make(map[string]struct{}, len(expected))
	for a := range expected {
		managed[a] = struct{}{}
	}
	h.maybeAutoFreeze(ctx, accountID, drifts, managed)
	return nil
}

// freezeToleranceBps is the auto-freeze (kill_switch) line — strictly
// higher than reconcileToleranceBps (the 50bps ledger/alert line) because
// auto-freezing halts a LIVE trading agent and a false positive is
// expensive. [INVENTED v1: tune as real drift data arrives.]
const freezeToleranceBps = 200.0

// freezeDebounceReports is how many CONSECUTIVE delta_reports must breach
// the freeze line before the kill fires — a single in-flight-fill blip at
// the 60s cadence must not halt the agent (Freqtrade-style debounce).
// [INVENTED v1]
const freezeDebounceReports = 2

// killedSentinel marks an account already auto-frozen this drift episode
// so we don't re-fire every subsequent report. Cleared when drift falls
// back below the freeze line. The agent-side latch is itself cleared only
// by restarting the process (§5.13); this sentinel just throttles repeats.
const killedSentinel = -1

// maxFlaggedDriftBps returns the largest flagged drift in bps among the
// MANAGED assets (0 if none). `managed` scopes the auto-freeze decision to
// the assets this account's instances actually track (their pair's base +
// USDT). Exchange-only balances the ledger never managed — e.g. a testnet
// faucet's hundreds of unrelated coins — are still recorded as
// discrepancies for the forensic trail by reconcile(), but they must not
// halt a live agent, so they don't count here. An empty `managed` set
// yields 0 (nothing to freeze on). Only flagged drifts count — they already
// cleared the dust floor, so a huge relative drift near zero can't trip
// the freeze.
func maxFlaggedDriftBps(drifts []driftResult, managed map[string]struct{}) float64 {
	max := 0.0
	for _, d := range drifts {
		if _, ok := managed[d.Asset]; !ok {
			continue
		}
		if d.Flagged && d.DriftBps > max {
			max = d.DriftBps
		}
	}
	return max
}

// nextDriftStreak advances the per-account debounce counter. prev<0 is the
// killedSentinel (already fired — suppress repeats). A report below the
// freeze line resets to 0, which also lifts the sentinel so a recurrence
// can re-arm.
func nextDriftStreak(maxBps float64, prev int) int {
	if maxBps < freezeToleranceBps {
		return 0
	}
	if prev < 0 {
		return prev
	}
	return prev + 1
}

// maybeAutoFreeze advances the drift streak for accountID and, when it
// reaches the debounce threshold, sends a discrepancy_detected kill_switch.
// Only drift on `managed` assets (the account's instances' tracked assets)
// arms the streak — see maxFlaggedDriftBps. No-op when no killer is wired.
// On send success the streak latches to killedSentinel (no repeats); on
// failure it stays armed so the next report retries.
func (h *agentMessageHandler) maybeAutoFreeze(ctx context.Context, accountID string, drifts []driftResult, managed map[string]struct{}) {
	maxBps := maxFlaggedDriftBps(drifts, managed)

	h.freezeMu.Lock()
	next := nextDriftStreak(maxBps, h.driftStreak[accountID])
	h.driftStreak[accountID] = next
	h.freezeMu.Unlock()

	if next < freezeDebounceReports || h.killer == nil {
		return
	}

	ks := wire.KillSwitch{
		Reason:         wire.KillSwitchDiscrepancyDetected,
		OperatorUserID: "system", // [INVENTED v1] auto-trigger sentinel, not a human
		Scope:          wire.KillSwitchScopeAll,
	}
	if err := h.killer.SendKillSwitch(accountID, ks); err != nil {
		// Account offline or send failed — leave the streak armed so the
		// next report retries; alert out-of-band (the agent is unmanaged).
		h.logger.Error("auto_freeze_send_failed",
			"account_id", accountID, "max_drift_bps", maxBps, "err", err)
		return
	}
	h.freezeMu.Lock()
	h.driftStreak[accountID] = killedSentinel
	h.freezeMu.Unlock()
	h.logger.Warn("auto_freeze_triggered",
		"account_id", accountID, "max_drift_bps", maxBps,
		"debounce_reports", freezeDebounceReports)
	recordKillAudit(ctx, h.auditor, h.logger, "system", accountID, ks,
		map[string]any{"trigger": "auto", "max_drift_bps": maxBps})
}
