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

	// freezeToleranceBps / freezeDebounceReports override the auto-freeze
	// thresholds (config.ReconcileConfig); main.go sets them post-construction.
	// Zero ⇒ the default* consts (see effFreezeToleranceBps /
	// effFreezeDebounceReports), so directly-constructed test handlers keep
	// the 200bps / N=2 behavior without wiring config.
	freezeToleranceBps    float64
	freezeDebounceReports int
}

// effFreezeToleranceBps is the configured auto-freeze line, or the default
// when unset (≤0). Keeping the fallback here means tests that build the
// handler struct literally don't have to set it.
func (h *agentMessageHandler) effFreezeToleranceBps() float64 {
	if h.freezeToleranceBps > 0 {
		return h.freezeToleranceBps
	}
	return defaultFreezeToleranceBps
}

// effFreezeDebounceReports is the configured debounce count, or the default
// when unset (<1).
func (h *agentMessageHandler) effFreezeDebounceReports() int {
	if h.freezeDebounceReports >= 1 {
		return h.freezeDebounceReports
	}
	return defaultFreezeDebounceReports
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
	// execution rows in place for auditing. Deduped: the exchange event
	// stream is at-least-once (Binance WS API replays execution reports on
	// resubscribe/reconnect) and each replay rides a fresh envelope msg_id,
	// so envelope-level replay protection can't catch it — a content-level
	// guard is what keeps a redelivered fill from inserting a second
	// SpotExecution row, whose fresh auto-increment ID the Tick ledger-fold
	// (③) would otherwise double-count into a position drift that
	// auto-freezes the agent within two reports.
	for i, f := range ou.Fills {
		// order_update fills carry no order id of their own — the enclosing
		// OrderUpdate names the order (§5.10).
		if _, err := h.insertFillIfNew(ctx, ou.ClientOrderID, ou.ExchangeOrderID, f); err != nil {
			return fmt.Errorf("agentmsg: order_update fill[%d]: %w", i, err)
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

// buildSpotExecutionFrom turns a wire.Fill (decimal strings) into a
// store.SpotExecution (float64 in SI precision), with the order identity
// passed explicitly: order_update fills carry no order id of their own (the
// enclosing OrderUpdate names the order, §5.10) while delta_report fills
// (§5.11) carry their own ClientOrderID/ExchangeOrderID. The decimal→float
// cast is lossy but acceptable per docs/saas-ws-protocol-v1.md §2.2 "numeric
// exception": SaaS-side bookkeeping uses float64, the wire uses strings only
// to avoid JSON float rounding.
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
		TradeID:            f.TradeID,
	}, nil
}

// insertFillIfNew persists a fill as a SpotExecution unless an equivalent one
// is already stored, returning whether it actually inserted. Both fill
// channels — order_update (hot path) and delta_report (fallback) — funnel
// through this single chokepoint so the "exactly one row per fill" invariant
// cannot be half-applied: a fill confirmed on one channel is skipped when it
// also arrives on the other, and a stream-redelivered fill (Binance WS API is
// at-least-once) is skipped on replay. The dedup is content-level because a
// redelivered fill rides a fresh envelope msg_id, which envelope replay
// protection cannot catch.
//
// Dedup identity: the exchange's per-trade id when present
// (client_order_id, trade_id). This is the ONLY correct key — a single market
// order sweeping a thin book produces several genuine fills that all share
// filled_at_exchange_ms, so an ms key would collapse them into one row and
// under-count the position into a reconciliation auto-freeze. When the backend
// surfaces no trade id (trade_id==0: MockExchange, legacy rows) we fall back to
// the (client_order_id, ms) key — acceptable there because those sources don't
// produce same-ms multi-fills.
func (h *agentMessageHandler) insertFillIfNew(ctx context.Context, clientOrderID, exchangeOrderID string, f wire.Fill) (bool, error) {
	var exists bool
	var err error
	if f.TradeID != 0 {
		exists, err = h.trades.ExecutionExistsByTrade(ctx, clientOrderID, f.TradeID)
	} else {
		exists, err = h.trades.ExecutionExists(ctx, clientOrderID, f.FilledAtExchangeMs)
	}
	if err != nil {
		return false, fmt.Errorf("fill dedup check: %w", err)
	}
	if exists {
		return false, nil
	}
	ex, err := buildSpotExecutionFrom(clientOrderID, exchangeOrderID, f)
	if err != nil {
		return false, err
	}
	if err := h.trades.InsertSpotExecution(ctx, ex); err != nil {
		return false, err
	}
	return true, nil
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
// parsePositions folds a delta_report's per-asset free+locked decimal
// strings into an asset→total float map (the exchange-truth holdings).
// Shared by reconcilePositions (drift math) and fundInstance (genesis
// seeding) so both read the snapshot identically. [INVENTED v1: free+locked]
func parsePositions(positions []wire.Position) (map[string]float64, error) {
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
	return actual, nil
}

func reconcilePositions(expected map[string]float64, positions []wire.Position) ([]driftResult, error) {
	actual, err := parsePositions(positions)
	if err != nil {
		return nil, err
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
		inserted, err := h.insertFillIfNew(ctx, f.ClientOrderID, f.ExchangeOrderID, f)
		if err != nil {
			return fmt.Errorf("agentmsg: delta_report fill[%d]: %w", i, err)
		}
		if !inserted {
			continue // already persisted via order_update — skip status bump + log
		}
		// ①: a recovered fill proves the order executed, so unstick a
		// still-pending TradeRecord. delta_report fills carry no order-level
		// status, so we can only assert partial_filled here; the authoritative
		// terminal status (filled/cancelled) stays with the order_update
		// channel, whose unconditional update overrides this if it arrives.
		if err := h.trades.MarkPartialIfPending(ctx, f.ClientOrderID); err != nil {
			return fmt.Errorf("agentmsg: delta_report advance status: %w", err)
		}
		h.logger.Warn("delta_report_recovered_fill",
			"account_id", accountID, "client_order_id", f.ClientOrderID,
			"filled_at_exchange_ms", f.FilledAtExchangeMs)
	}
	return nil
}

// reconcile aggregates the SaaS-side expected holdings across the
// account's instances' latest portfolio snapshots, diffs them against the
// exchange-truth positions, and persists any flagged drift. An instance not
// yet funded (FundedAtMs NULL) is first anchored to the exchange snapshot
// (fundInstance) and excluded from this round, so a fresh instance's $0
// ledger never false-positives its real exchange balance as total drift.
func (h *agentMessageHandler) reconcile(
	ctx context.Context, accountID, instanceID string,
	insts []store.StrategyInstance, dr *wire.DeltaReport,
) error {
	// Exchange-truth holdings, parsed once: genesis funding seeds from it,
	// reconcilePositions re-derives it for the drift math.
	actual, err := parsePositions(dr.Positions)
	if err != nil {
		return fmt.Errorf("agentmsg: delta_report parse positions: %w", err)
	}
	now := time.Now().UnixMilli()

	expected := map[string]float64{}
	hasBaseline := false
	for i := range insts {
		inst := &insts[i]
		// Genesis funding: an instance with no funded baseline yet anchors
		// its SaaS ledger to the exchange snapshot instead of reconciling a
		// $0 ledger against real holdings (the BTC/USDT baseline=0 trap).
		// It contributes nothing to this report's drift — next report
		// reconciles against the freshly seeded baseline.
		if inst.FundedAtMs == nil {
			if err := h.fundInstance(ctx, inst, actual, now); err != nil {
				return err
			}
			continue
		}
		ps, err := h.portfolios.Latest(ctx, inst.InstanceID)
		if err != nil {
			return fmt.Errorf("agentmsg: delta_report portfolio latest: %w", err)
		}
		if ps == nil {
			continue // funded flag set but seed row missing — skip this round
		}
		hasBaseline = true
		base := strings.TrimSuffix(inst.Pair, "USDT") // [INVENTED v1] base asset from pair
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

	// Observability for threshold tuning (可观测): one structured line per
	// reconcile pairing the freeze-decision input (max_managed_drift_bps, the
	// value compared against the line) with the trade-speed proxy for this
	// window (window_fills) and the active thresholds. Charting drift vs fills
	// across reports is how the [INVENTED v1] freeze line gets a data-backed
	// value — a faster strategy's normal in-flight drift sits higher.
	flaggedCount := 0
	for _, d := range drifts {
		if d.Flagged {
			flaggedCount++
		}
	}
	h.logger.Info("delta_report_reconcile_summary",
		"account_id", accountID, "instance_id", instanceID,
		"managed_assets", len(managed),
		"max_managed_drift_bps", maxFlaggedDriftBps(drifts, managed),
		"flagged_count", flaggedCount,
		"window_fills", len(dr.SinceLastReport.Fills),
		"freeze_line_bps", h.effFreezeToleranceBps(),
		"debounce_reports", h.effFreezeDebounceReports())

	h.maybeAutoFreeze(ctx, accountID, drifts, managed)
	return nil
}

// buildSeedPortfolio builds the genesis ledger row for a fresh instance from
// the exchange-truth holdings. The exchange reports only raw assets, so the
// three-state BTC split seeds everything into FloatBTC (the active trading
// float); DeadBTC/ColdSealedBTC are SaaS-internal lifecycle buckets the
// strategy grows into from here, genesis zero. LastProcessedBarTime stays 0 so
// the next Tick loads the full trailing window. Pure (no DB/clock) for tests.
// [INVENTED v1: whole-balance anchor; one instance per exchange account]
func buildSeedPortfolio(inst *store.StrategyInstance, actual map[string]float64, nowMs int64) *store.PortfolioState {
	base := strings.TrimSuffix(inst.Pair, "USDT")
	return &store.PortfolioState{
		InstanceID: inst.InstanceID,
		NowMs:      nowMs,
		FloatBTC:   actual[base],
		USDT:       actual["USDT"],
	}
}

// fundInstance claims the genesis funding slot (MarkFunded NULL guard) and —
// only when the claim succeeds — anchors the SaaS ledger to the exchange's
// actual holdings. Claim-first prevents concurrent double-seed: if two
// delta_reports both see FundedAtMs=NULL and race here, only the winner
// (MarkFunded RowsAffected=1) writes the seed row. Mutates inst.FundedAtMs
// so the caller's loop does not also reconcile it this round.
func (h *agentMessageHandler) fundInstance(ctx context.Context, inst *store.StrategyInstance, actual map[string]float64, nowMs int64) error {
	claimed, err := h.instances.MarkFunded(ctx, inst.InstanceID, nowMs)
	if err != nil {
		return fmt.Errorf("agentmsg: mark instance %s funded: %w", inst.InstanceID, err)
	}
	if !claimed {
		return nil // another goroutine already claimed; skip seed write
	}
	seed := buildSeedPortfolio(inst, actual, nowMs)
	// Anchor the ledger watermark to the latest existing fill: the genesis
	// balance already reflects every pre-funding execution, so the first Tick
	// must not double-count them when it folds fills into the ledger (③).
	maxExecID, err := h.trades.MaxExecutionIDForInstance(ctx, inst.InstanceID)
	if err != nil {
		return fmt.Errorf("agentmsg: fund instance %s max exec id: %w", inst.InstanceID, err)
	}
	seed.LastAppliedExecID = maxExecID
	if err := h.portfolios.Append(ctx, seed); err != nil {
		return fmt.Errorf("agentmsg: fund instance %s seed portfolio: %w", inst.InstanceID, err)
	}
	inst.FundedAtMs = &nowMs
	h.logger.Info("instance_funded_from_exchange",
		"account_id", inst.AccountID, "instance_id", inst.InstanceID,
		"base_asset", strings.TrimSuffix(inst.Pair, "USDT"),
		"float_btc", seed.FloatBTC, "usdt", seed.USDT)
	return nil
}

// defaultFreezeToleranceBps is the auto-freeze (kill_switch) line — strictly
// higher than reconcileToleranceBps (the 50bps ledger/alert line) because
// auto-freezing halts a LIVE trading agent and a false positive is expensive.
// Overridable via config.ReconcileConfig.FreezeToleranceBps (see effFreeze*);
// this const is the fallback when unset. [INVENTED v1: tune as real drift
// data arrives — chart delta_report_reconcile_summary's max_managed_drift_bps
// against window_fills to pick a line above normal in-flight drift.]
const defaultFreezeToleranceBps = 200.0

// defaultFreezeDebounceReports is how many CONSECUTIVE delta_reports must
// breach the freeze line before the kill fires — a single in-flight-fill
// blip at the 60s cadence must not halt the agent (Freqtrade-style debounce).
// Overridable via config (see effFreeze*). [INVENTED v1]
const defaultFreezeDebounceReports = 2

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
func nextDriftStreak(maxBps float64, prev int, freezeBps float64) int {
	if maxBps < freezeBps {
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
	next := nextDriftStreak(maxBps, h.driftStreak[accountID], h.effFreezeToleranceBps())
	h.driftStreak[accountID] = next
	h.freezeMu.Unlock()

	if next < h.effFreezeDebounceReports() || h.killer == nil {
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
		"debounce_reports", h.effFreezeDebounceReports())
	recordKillAudit(ctx, h.auditor, h.logger, "system", accountID, ks,
		map[string]any{"trigger": "auto", "max_drift_bps": maxBps})
}

// ClearDriftStreak resets the auto-freeze debounce counter for accountID,
// lifting any killedSentinel so a subsequent sustained drift can re-arm
// and re-fire maybeAutoFreeze. Called on resume (§5.13 v2): without it a
// resumed account whose drift persists would never auto-freeze again until
// one clean report happened to intervene, defeating the safety net.
func (h *agentMessageHandler) ClearDriftStreak(accountID string) {
	h.freezeMu.Lock()
	delete(h.driftStreak, accountID)
	h.freezeMu.Unlock()
}
