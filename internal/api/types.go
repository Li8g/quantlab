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
