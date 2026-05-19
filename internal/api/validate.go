package api

import (
	"errors"

	"quantlab/internal/resultpkg"
)

// allowedIntervals is the prototype-phase whitelist for K-line bar
// intervals. Strategies bound to one interval (see sigmoid_v1.New) must
// receive a value from this set; the SaaS Epoch service rejects others.
var allowedIntervals = map[string]bool{
	"1m": true, "5m": true, "15m": true,
	"1h": true, "4h": true,
	"1d": true,
}

// Validate checks the field-level invariants documented on
// CreateEvolutionTaskRequest. Cross-field checks live here too
// (e.g. SpawnMode=manual ⇒ SpawnPoint != nil).
func (r *CreateEvolutionTaskRequest) Validate() error {
	if r == nil {
		return errors.New("CreateEvolutionTaskRequest is nil")
	}
	if r.StrategyID == "" {
		return errors.New("strategy_id is required")
	}
	if r.Pair == "" {
		return errors.New("pair is required")
	}
	if r.Interval == "" {
		return errors.New("interval is required")
	}
	if !allowedIntervals[r.Interval] {
		return errors.New("interval must be one of 1m,5m,15m,1h,4h,1d")
	}
	if r.PopSize < 1 {
		return errors.New("pop_size must be >= 1")
	}
	if r.MaxGenerations < 1 {
		return errors.New("max_generations must be >= 1")
	}
	if r.EliteRatio < 0 || r.EliteRatio > 1 {
		return errors.New("elite_ratio must be in [0, 1]")
	}
	if r.FatalMDD < 0 || r.FatalMDD > 1 {
		return errors.New("fatal_mdd must be in [0, 1]")
	}
	if r.TakerFeeBPS < 0 {
		return errors.New("taker_fee_bps must be >= 0")
	}
	if r.SlippageBPS < 0 {
		return errors.New("slippage_bps must be >= 0")
	}
	if !r.SpawnMode.IsValid() {
		return errors.New("spawn_mode must be one of inherit/random_once/manual")
	}
	if r.SpawnMode == resultpkg.SpawnModeManual && r.SpawnPoint == nil {
		return errors.New("spawn_point is required when spawn_mode=manual")
	}
	if r.SpawnMode != resultpkg.SpawnModeManual && r.SpawnPoint != nil {
		return errors.New("spawn_point must be omitted unless spawn_mode=manual")
	}
	if r.FatalAuditSampleRate != nil {
		v := *r.FatalAuditSampleRate
		if v < 0 || v > 1 {
			return errors.New("fatal_audit_sample_rate must be in [0, 1]")
		}
	}
	if r.OosDays != nil && *r.OosDays < 1 {
		return errors.New("oos_days, if set, must be >= 1")
	}
	return nil
}

// Validate enforces that a Promote request has a non-empty ReviewedBy.
func (r *PromoteChallengerRequest) Validate() error {
	if r == nil {
		return errors.New("PromoteChallengerRequest is nil")
	}
	if r.ReviewedBy == "" {
		return errors.New("reviewed_by is required")
	}
	return nil
}

// Validate enforces that a Retire request has a non-empty ReviewedBy.
func (r *RetireChampionRequest) Validate() error {
	if r == nil {
		return errors.New("RetireChampionRequest is nil")
	}
	if r.ReviewedBy == "" {
		return errors.New("reviewed_by is required")
	}
	return nil
}
