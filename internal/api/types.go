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

	// SpawnPoint is required when SpawnMode == "manual" and must be nil
	// otherwise. Engine passes the raw bytes through to the strategy's
	// SpawnPoint decoder; api/handlers do not inspect the payload.
	SpawnPoint *json.RawMessage `json:"spawn_point,omitempty"`
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
