// Phase 9 batch 1 handlers — read-only diagnostics + lists. Registered
// from handlers.go.Register when the corresponding collaborator is
// non-nil. Source of truth for routes:
// docs/Coding-plan-dev-phases-prompts_v3_2_2.md Phase 9 route table.
//
// Common conventions:
//   - All list endpoints accept ?limit=N with a per-endpoint default
//     and a hard cap. Out-of-range values are clamped silently — bad
//     UX to reject "?limit=99999".
//   - Response envelopes always carry both `items` and `count`. count
//     equals len(items) — clients can detect truncation by comparing
//     count to the limit they sent.
//   - Errors follow the existing {"error": "..."} convention via
//     writeError + the mapReadErr helper.
package api

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	"quantlab/internal/api/middleware"
	"quantlab/internal/saas/store"
	"quantlab/internal/verification"
)

// Per-endpoint pagination defaults and hard caps. Defaults are tuned
// for a "show me the recent state" UX; caps bound payload size on
// instances with long histories.
const (
	defaultTaskListLimit   = 50
	maxTaskListLimit       = 200
	defaultChampionHistory = 50
	maxChampionHistory     = 500
	defaultGapsLimit       = 100
	maxGapsLimit           = 1000
	defaultInstanceTrades  = 100
	maxInstanceTrades      = 1000
)

// parseLimit reads ?limit=N. Empty / non-numeric falls back to def;
// values > max are clamped to max; values ≤ 0 fall back to def.
func parseLimit(raw string, def, max int) int {
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	if v > max {
		return max
	}
	return v
}

// ===== /api/v1/evolution/tasks (list) =====

// ListTasks: GET /api/v1/evolution/tasks?limit=N. Returns most recent
// N tasks ordered by created_at descending. No auth in v1 — same
// surface as the per-task status endpoint.
func (h *Handlers) ListTasks(c *gin.Context) {
	limit := parseLimit(c.Query("limit"), defaultTaskListLimit, maxTaskListLimit)
	rows, err := h.TaskLister.List(c.Request.Context(), limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]EvolutionTaskSummary, 0, len(rows))
	for _, r := range rows {
		items = append(items, EvolutionTaskSummary{
			TaskID:     r.TaskID,
			StrategyID: r.StrategyID,
			Pair:       r.Pair,
			Interval:   r.Interval,
			Status:     string(r.Status),
			CreatedAt:  r.CreatedAt.UnixMilli(),
		})
	}
	c.JSON(http.StatusOK, ListTasksResponse{Items: items, Count: len(items)})
}

// ===== /api/v1/champions/history =====

// ListChampionHistory: GET /api/v1/champions/history?strategy_id=&pair=&limit=N.
// Both filter params are optional. Newest first.
func (h *Handlers) ListChampionHistory(c *gin.Context) {
	strategyID := c.Query("strategy_id")
	pair := c.Query("pair")
	limit := parseLimit(c.Query("limit"), defaultChampionHistory, maxChampionHistory)

	rows, err := h.ChampionHistory.List(c.Request.Context(), strategyID, pair, limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]ChampionHistoryEntry, 0, len(rows))
	for _, r := range rows {
		entry := ChampionHistoryEntry{
			ID:           r.ID,
			StrategyID:   r.StrategyID,
			Pair:         r.Pair,
			ChallengerID: r.ChallengerID,
			PromotedAtMs: r.PromotedAt.UnixMilli(),
			RetiredBy:    r.RetiredBy,
			RetireNote:   r.RetireNote,
		}
		if r.RetiredAt != nil {
			ms := r.RetiredAt.UnixMilli()
			entry.RetiredAtMs = &ms
		}
		items = append(items, entry)
	}
	c.JSON(http.StatusOK, ListChampionHistoryResponse{Items: items, Count: len(items)})
}

// ===== /api/v1/genome/champion =====

// GetChampionGenome: GET /api/v1/genome/champion?strategy_id=&pair=.
// Both query params are REQUIRED. Returns 404 when no active champion
// exists for the pair. Returns the active champion's metadata plus
// score snapshot from the linked GeneRecord; full chromosome JSON
// stays in the /package endpoint to keep this response small.
func (h *Handlers) GetChampionGenome(c *gin.Context) {
	strategyID := c.Query("strategy_id")
	pair := c.Query("pair")
	if strategyID == "" || pair == "" {
		writeError(c, http.StatusBadRequest,
			errors.New("strategy_id and pair are required"))
		return
	}
	champ, err := h.ChampionHistory.GetActive(c.Request.Context(), strategyID, pair)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	resp := ChampionGenomeResponse{
		StrategyID:   champ.StrategyID,
		Pair:         champ.Pair,
		ChallengerID: champ.ChallengerID,
		PromotedAtMs: champ.PromotedAt.UnixMilli(),
	}
	// Best-effort score enrichment. The GeneRecord MUST exist if a
	// ChampionHistory row points at it (Promote runs both inserts in
	// one tx) — but a corrupted state shouldn't 500 the whole
	// endpoint; fall through with score fields nil instead.
	if h.Challengers != nil {
		if rec, err := h.Challengers.Get(c.Request.Context(), champ.ChallengerID); err == nil && rec != nil {
			resp.ScoreTotal = rec.ScoreTotal
			resp.PlanHash = rec.PlanHash
			resp.BarsHash = rec.BarsHash
		}
	}
	c.JSON(http.StatusOK, resp)
}

// ===== /api/v1/data/gaps =====

// ListGaps: GET /api/v1/data/gaps?symbol=&interval=&limit=N. Both
// symbol and interval REQUIRED — the kline_gaps table is keyed by
// (symbol, interval) and unfiltered listings would mix unrelated
// pairs in one payload.
func (h *Handlers) ListGaps(c *gin.Context) {
	symbol := c.Query("symbol")
	interval := c.Query("interval")
	if symbol == "" || interval == "" {
		writeError(c, http.StatusBadRequest,
			errors.New("symbol and interval are required"))
		return
	}
	limit := parseLimit(c.Query("limit"), defaultGapsLimit, maxGapsLimit)

	rows, err := h.Gaps.List(c.Request.Context(), symbol, interval, limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]KLineGapResponse, 0, len(rows))
	for _, r := range rows {
		items = append(items, KLineGapResponse{
			Symbol:       r.Symbol,
			Interval:     r.Interval,
			GapStartMs:   r.GapStartMs,
			GapEndMs:     r.GapEndMs,
			DurationMs:   r.GapEndMs - r.GapStartMs,
			DetectedAtMs: r.DetectedAt.UnixMilli(),
		})
	}
	c.JSON(http.StatusOK, ListGapsResponse{Items: items, Count: len(items)})
}

// ===== /api/v1/data/coverage =====

// GetCoverage: GET /api/v1/data/coverage?symbol=&interval=. Hybrid
// shape — with both params it returns the single matching pair's
// coverage (empty items if that pair has no bars); with neither it
// returns one row per (symbol, interval) in the klines table, the
// data-inventory view the frontend symbol picker reads. Passing
// exactly one of the two is a 400: a lone symbol/interval is almost
// always a client bug, and silently widening to "all pairs" would
// mask it. No ?limit — row count is bounded by the number of pairs.
func (h *Handlers) GetCoverage(c *gin.Context) {
	symbol := c.Query("symbol")
	interval := c.Query("interval")
	if (symbol == "") != (interval == "") {
		writeError(c, http.StatusBadRequest,
			errors.New("symbol and interval must be provided together or both omitted"))
		return
	}
	rows, err := h.Klines.Coverage(c.Request.Context(), symbol, interval)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]DataCoverageEntry, 0, len(rows))
	for _, r := range rows {
		items = append(items, DataCoverageEntry(r))
	}
	c.JSON(http.StatusOK, ListCoverageResponse{Items: items, Count: len(items)})
}

// ===== /api/v1/instances/:instance_id/trades =====

// ListInstanceTrades: GET /api/v1/instances/:instance_id/trades?limit=N.
// Ownership: viewer/operator see only instances they own; admin sees
// any instance. Returns 404 if the instance doesn't exist, 403 on
// ownership violation.
func (h *Handlers) ListInstanceTrades(c *gin.Context) {
	id := c.Param("instance_id")
	if id == "" {
		writeError(c, http.StatusBadRequest, errors.New("instance_id is required"))
		return
	}
	inst, err := h.Instances.Get(c.Request.Context(), id)
	if err != nil {
		writeError(c, mapReadErr(err), err)
		return
	}
	if !canViewInstance(c, inst) {
		writeError(c, http.StatusForbidden,
			errors.New("instance owned by another user"))
		return
	}

	limit := parseLimit(c.Query("limit"), defaultInstanceTrades, maxInstanceTrades)
	rows, err := h.Trades.ListByInstance(c.Request.Context(), id, limit)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	items := make([]TradeRecordSummary, 0, len(rows))
	for _, r := range rows {
		items = append(items, toTradeSummary(r))
	}
	c.JSON(http.StatusOK, ListInstanceTradesResponse{Items: items, Count: len(items)})
}

// ===== /api/v1/ga/sharpebank/stats =====

// GetSharpeBankStats: GET /api/v1/ga/sharpebank/stats?strategy_id=&pair=.
// Both query params are REQUIRED — Stats is keyed by (strategy_id,
// pair_id) and the SharpeBank table doesn't index by either alone.
// dsr_eligible is derived (N >= verification.MinTrialsForDSR) so the
// UI doesn't have to import the verification constant to render the
// gate.
func (h *Handlers) GetSharpeBankStats(c *gin.Context) {
	strategyID := c.Query("strategy_id")
	pair := c.Query("pair")
	if strategyID == "" || pair == "" {
		writeError(c, http.StatusBadRequest,
			errors.New("strategy_id and pair are required"))
		return
	}
	snap, err := h.SharpeBank.Stats(c.Request.Context(), strategyID, pair)
	if err != nil {
		writeError(c, http.StatusInternalServerError, err)
		return
	}
	c.JSON(http.StatusOK, SharpeBankStatsResponse{
		StrategyID:      strategyID,
		Pair:            pair,
		N:               snap.N,
		SharpeMean:      snap.SharpeMean,
		SharpeVariance:  snap.SharpeVariance,
		MinTrialsForDSR: verification.MinTrialsForDSR,
		DSREligible:     snap.N >= verification.MinTrialsForDSR,
	})
}

// canViewInstance enforces ownership for read-only endpoints. Tests
// running without auth middleware see ownership pass through (the
// claims absent path returns true to keep test fixtures simple — auth
// gating is a separate concern handled at Register time).
func canViewInstance(c *gin.Context, inst *store.StrategyInstance) bool {
	claims, ok := middleware.ClaimsFrom(c)
	if !ok {
		return true
	}
	// Admin role (step-up token) or admin-capable standing session both
	// see every instance; everyone else is scoped to their own.
	if store.UserRole(claims.Role) == store.UserRoleAdmin || claims.AdminCapable {
		return true
	}
	return claims.UserID == inst.OwnerUserID
}
