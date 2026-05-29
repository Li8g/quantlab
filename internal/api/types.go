// Package api holds HTTP request/response struct contracts for QuantLab's
// public surface. Field names, JSON tags, and required/nullable semantics
// are frozen at schema v5.3.3 — breaking changes require a SchemaVersion bump.
package api

import (
	"encoding/json"

	"quantlab/internal/resultpkg"
)

// CreateEvolutionTaskRequest is the body of POST /api/v1/evolution/tasks.
//
// Required (value-typed) vs optional (pointer) fields encode the contract:
// presence of a pointer field signals user intent; nil means "use default".
//
// TestMode=true forces the engine to write TakerFeeBPS=0 and SlippageBPS=0
// into GAConfigSnapshot. The user's original request values are recorded
// on EvolutionTask.RequestedTakerFeeBPS / RequestedSlippageBPS for audit.
// Challengers from TestMode=true tasks cannot be Promoted.
type CreateEvolutionTaskRequest struct {
	StrategyID           string              `json:"strategy_id"`
	Pair                 string              `json:"pair"`
	Interval             string              `json:"interval"`
	PopSize              int                 `json:"pop_size"`
	MaxGenerations       int                 `json:"max_generations"`
	EliteRatio           float64             `json:"elite_ratio"`
	FatalMDD             float64             `json:"fatal_mdd"`
	TakerFeeBPS          float64             `json:"taker_fee_bps"`
	SlippageBPS          float64             `json:"slippage_bps"`
	SpawnMode            resultpkg.SpawnMode `json:"spawn_mode"`
	TestMode             bool                `json:"test_mode"`
	OosDays              *int                `json:"oos_days,omitempty"`
	FatalAuditSampleRate *float64            `json:"fatal_audit_sample_rate,omitempty"`

	// Plan-construction overrides. All nil → server-side defaults from
	// epoch.DefaultDefaults() (WarmupDays=365, LotStep=LotMin=0.00001,
	// InitialUSDT=10000, DCA={10000, 0}). Setting any of them is a
	// per-task override; engine merges these into PlanOptions before
	// building the EvaluablePlan.
	WarmupDays  *int              `json:"warmup_days,omitempty"`
	LotStep     *float64          `json:"lot_step,omitempty"`
	LotMin      *float64          `json:"lot_min,omitempty"`
	InitialUSDT *float64          `json:"initial_usdt,omitempty"`
	DCA         *DCAConfigRequest `json:"dca,omitempty"`

	// SpawnPoint is required when SpawnMode == "manual" and must be nil
	// otherwise. Engine passes the raw bytes through to the strategy's
	// SpawnPoint decoder; api/handlers do not inspect the payload.
	SpawnPoint *json.RawMessage `json:"spawn_point,omitempty"`
}

// DCAConfigRequest is the wire shape of the optional `dca` field on
// CreateEvolutionTaskRequest. Mirrors fitness.GhostDCAConfig but lives
// in api so the wire boundary doesn't depend on the fitness package.
type DCAConfigRequest struct {
	InitialCapital float64 `json:"initial_capital"`
	MonthlyInject  float64 `json:"monthly_inject"`
}

// EvolutionTaskStatusResponse is the body of
// GET /api/v1/evolution/tasks/:task_id.
type EvolutionTaskStatusResponse struct {
	TaskID            string               `json:"task_id"`
	Status            resultpkg.TaskStatus `json:"status"`
	CurrentGeneration int                  `json:"current_generation"`
	BestScore         *float64             `json:"best_score,omitempty"`
	ChallengerID      *string              `json:"challenger_id,omitempty"`
	FailureReason     *string              `json:"failure_reason,omitempty"`
}

// CreateEvolutionTaskResponse is the body of the 202 Accepted that
// POST /api/v1/evolution/tasks returns. Clients poll
// GET /api/v1/evolution/tasks/:task_id for progress.
type CreateEvolutionTaskResponse struct {
	TaskID string `json:"task_id"`
}

// ChallengerSummaryResponse is the body of
// GET /api/v1/challengers/:challenger_id. Fields are lifted from
// GeneRecord top-level columns — for the full ChallengerResultPackage
// (the verbatim JSON blob), callers fetch
// GET /api/v1/challengers/:challenger_id/package.
//
// score_total / score_raw / consistency_penalty / max_drawdown are nil
// for Fatal aggregate results (SliceScore three-state semantics).
//
// retired_at_ms is lifted from champion_histories (NOT decision_status —
// the enum is locked to pending/promoted/rejected per spec). A
// challenger is currently the active champion iff
// decision_status="promoted" AND retired_at_ms is nil.
type ChallengerSummaryResponse struct {
	ChallengerID       string                   `json:"challenger_id"`
	StrategyID         string                   `json:"strategy_id"`
	Pair               string                   `json:"pair"`
	ScoreTotal         *float64                 `json:"score_total,omitempty"`
	ScoreRaw           *float64                 `json:"score_raw,omitempty"`
	ConsistencyPenalty *float64                 `json:"consistency_penalty,omitempty"`
	DecisionStatus     resultpkg.DecisionStatus `json:"decision_status"`
	PlanHash           string                   `json:"plan_hash"`
	BarsHash           string                   `json:"bars_hash"`
	TestMode           bool                     `json:"test_mode"`
	DSR                *float64                 `json:"dsr,omitempty"`
	PromotedAtMs       *int64                   `json:"promoted_at_ms,omitempty"`
	RetiredAtMs        *int64                   `json:"retired_at_ms,omitempty"`
}

// PromoteChallengerRequest is the body of
// POST /api/v1/challengers/:challenger_id/promote.
type PromoteChallengerRequest struct {
	ReviewedBy   string  `json:"reviewed_by"`
	DecisionNote *string `json:"decision_note,omitempty"`
}

// RetireChampionRequest is the body of
// POST /api/v1/champions/:champion_id/retire.
type RetireChampionRequest struct {
	ReviewedBy   string  `json:"reviewed_by"`
	DecisionNote *string `json:"decision_note,omitempty"`
}

// ===================================================================
// Phase 6.3: Instance lifecycle endpoints
// ===================================================================

// CreateInstanceRequest is the body of POST /api/v1/instances. The
// instance is created in `idle` state — caller must POST /start
// (or first /deploy-champion + then /start) to make it tickable.
type CreateInstanceRequest struct {
	StrategyID string `json:"strategy_id"`
	Pair       string `json:"pair"`
	AccountID  string `json:"account_id"`
}

// InstanceResponse is the body of GET /api/v1/instances/:instance_id
// and the 201 Created response from POST /api/v1/instances.
type InstanceResponse struct {
	InstanceID       string  `json:"instance_id"`
	StrategyID       string  `json:"strategy_id"`
	Pair             string  `json:"pair"`
	AccountID        string  `json:"account_id"`
	OwnerUserID      uint    `json:"owner_user_id"`
	Status           string  `json:"status"`
	ActiveChampID    *string `json:"active_champion_id,omitempty"`
	LastTickWallTime *int64  `json:"last_tick_wall_time_ms,omitempty"`
}

// DeployChampionRequest is the body of
// POST /api/v1/instances/:instance_id/deploy-champion. Promote-then-
// Deploy is split per B2 — Promote alone does not touch instances.
type DeployChampionRequest struct {
	ChallengerID string `json:"challenger_id"`
}

// ===================================================================
// Phase 9 batch 1: read-only diagnostics endpoints
// ===================================================================

// KLineGapResponse is one row of GET /api/v1/data/gaps. Mirrors the
// store.KLineGap GORM columns; duration_ms is server-computed for
// convenience (= gap_end_ms - gap_start_ms).
type KLineGapResponse struct {
	Symbol       string `json:"symbol"`
	Interval     string `json:"interval"`
	GapStartMs   int64  `json:"gap_start_ms"`
	GapEndMs     int64  `json:"gap_end_ms"`
	DurationMs   int64  `json:"duration_ms"`
	DetectedAtMs int64  `json:"detected_at_ms"`
}

// ListGapsResponse is the body of GET /api/v1/data/gaps. count is the
// length of items; clients can use it to detect truncation when a
// limit was applied.
type ListGapsResponse struct {
	Items []KLineGapResponse `json:"items"`
	Count int                `json:"count"`
}

// DataCoverageEntry is one row of GET /api/v1/data/coverage — the
// stored-bar span and count for a (symbol, interval). min/max are the
// open_time of the earliest/latest bar in milliseconds.
type DataCoverageEntry struct {
	Symbol    string `json:"symbol"`
	Interval  string `json:"interval"`
	MinOpenMs int64  `json:"min_open_ms"`
	MaxOpenMs int64  `json:"max_open_ms"`
	BarCount  int64  `json:"bar_count"`
}

// ListCoverageResponse is the body of GET /api/v1/data/coverage. One
// item per matching (symbol, interval); count == len(items). Unlike
// /data/gaps there is no limit — the row count is bounded by the
// number of distinct pairs, not by history length.
type ListCoverageResponse struct {
	Items []DataCoverageEntry `json:"items"`
	Count int                 `json:"count"`
}

// EvolutionTaskSummary is one row of GET /api/v1/evolution/tasks.
// A subset of EvolutionTaskStatusResponse — list views don't carry
// challenger_id or failure_reason (clients drill into the per-task
// status endpoint for those).
type EvolutionTaskSummary struct {
	TaskID     string `json:"task_id"`
	StrategyID string `json:"strategy_id"`
	Pair       string `json:"pair"`
	Interval   string `json:"interval"`
	Status     string `json:"status"`
	CreatedAt  int64  `json:"created_at_ms"`
}

// ListTasksResponse is the body of GET /api/v1/evolution/tasks.
// Items are ordered by created_at_ms descending (newest first).
type ListTasksResponse struct {
	Items []EvolutionTaskSummary `json:"items"`
	Count int                    `json:"count"`
}

// ChampionHistoryEntry is one row of GET /api/v1/champions/history.
// retired_at_ms is nil for the currently-active champion(s).
type ChampionHistoryEntry struct {
	ID           uint    `json:"id"`
	StrategyID   string  `json:"strategy_id"`
	Pair         string  `json:"pair"`
	ChallengerID string  `json:"challenger_id"`
	PromotedAtMs int64   `json:"promoted_at_ms"`
	RetiredAtMs  *int64  `json:"retired_at_ms,omitempty"`
	RetiredBy    *string `json:"retired_by,omitempty"`
	RetireNote   *string `json:"retire_note,omitempty"`
}

// ListChampionHistoryResponse is the body of GET /api/v1/champions/history.
// Items are ordered by promoted_at descending.
type ListChampionHistoryResponse struct {
	Items []ChampionHistoryEntry `json:"items"`
	Count int                    `json:"count"`
}

// ChampionGenomeResponse is the body of GET /api/v1/genome/champion.
// Combines ChampionHistory metadata with the active champion's score
// summary. Clients that need the full chromosome / result package
// follow up with GET /api/v1/challengers/:challenger_id/package.
type ChampionGenomeResponse struct {
	StrategyID   string   `json:"strategy_id"`
	Pair         string   `json:"pair"`
	ChallengerID string   `json:"challenger_id"`
	PromotedAtMs int64    `json:"promoted_at_ms"`
	ScoreTotal   *float64 `json:"score_total,omitempty"`
	PlanHash     string   `json:"plan_hash"`
	BarsHash     string   `json:"bars_hash"`
}

// TradeRecordSummary is one row of GET /api/v1/instances/:instance_id/trades.
// Captures the order-intent shape from store.TradeRecord; per-fill
// detail (SpotExecution rows) is not included here to keep list
// payloads bounded — a future /trades/:client_order_id endpoint can
// drill in.
type TradeRecordSummary struct {
	ClientOrderID string   `json:"client_order_id"`
	Symbol        string   `json:"symbol"`
	Side          string   `json:"side"`
	OrderType     string   `json:"order_type"`
	QuantityUSD   float64  `json:"quantity_usd"`
	LimitPrice    *float64 `json:"limit_price,omitempty"`
	NowMsAtSaaS   int64    `json:"now_ms_at_saas"`
	ValidUntilMs  int64    `json:"valid_until_ms"`
	Status        string   `json:"status"`
	CreatedAtMs   int64    `json:"created_at_ms"`

	// Fills carries exchange-side fill detail, populated only on the
	// /live snapshot path (via ExecutionLister). The /trades list
	// leaves it nil, so the key is absent there.
	Fills []SpotExecutionSummary `json:"fills,omitempty"`
}

// ListInstanceTradesResponse is the body of
// GET /api/v1/instances/:instance_id/trades. Items are ordered by
// CreatedAt descending (newest first).
type ListInstanceTradesResponse struct {
	Items []TradeRecordSummary `json:"items"`
	Count int                  `json:"count"`
}

// SharpeBankStatsResponse is the body of GET /api/v1/ga/sharpebank/stats.
// Surfaces the §I-4.2 reliability stats so the SaaS UI can show DSR
// confidence to operators before they Promote: until N reaches
// MinTrialsForDSR, DSR cannot be computed from this bucket.
type SharpeBankStatsResponse struct {
	StrategyID      string  `json:"strategy_id"`
	Pair            string  `json:"pair"`
	N               int     `json:"n"`
	SharpeMean      float64 `json:"sharpe_mean"`
	SharpeVariance  float64 `json:"sharpe_variance"`
	MinTrialsForDSR int     `json:"min_trials_for_dsr"`
	DSREligible     bool    `json:"dsr_eligible"`
}
