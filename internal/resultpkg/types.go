package resultpkg

import "encoding/json"

// SliceScore is a single window's score under Sum-type semantics.
// Three mutually exclusive states (validated in CrucibleResult.Validate):
//
//	(1) Normal:          Fatal=false, Value!=nil, SkippedBy=nil
//	(2) Cascade-skipped: Fatal=false, Value=nil,  SkippedBy!=nil
//	(3) Self-Fatal:      Fatal=true,  Value=nil,  SkippedBy=nil
//
// ⚠️ Never dereference *Value directly when sorting; always use the
// engine's CompareFitness helper. Never write sentinel numbers
// (-99999, -1e18) into Value.
type SliceScore struct {
	Fatal  bool     `json:"fatal"`
	Value  *float64 `json:"value,omitempty"`
	Reason *string  `json:"reason,omitempty"`
}

// CrucibleScoreComponents is the per-window decomposition of a fitness
// score. Tightened to a struct in v2; alpha_breakdown for the full result
// package remains json.RawMessage until phase 2 collapses.
type CrucibleScoreComponents struct {
	MonthlyScore    *float64 `json:"monthly_score,omitempty"`
	WeeklyScore     *float64 `json:"weekly_score,omitempty"`
	BaseScore       *float64 `json:"base_score,omitempty"`
	TurnoverPenalty *float64 `json:"turnover_penalty,omitempty"`
}

// CrucibleResult is one window's evaluation outcome.
type CrucibleResult struct {
	Window        WindowName               `json:"window"`
	Score         SliceScore               `json:"score"`
	FatalReason   *string                  `json:"fatal_reason,omitempty"`
	FatalAtBarTS  *int64                   `json:"fatal_at_bar_ts,omitempty"`
	FatalMDDValue *float64                 `json:"fatal_mdd_value,omitempty"`
	BarsEvaluated int                      `json:"bars_evaluated"`
	SkippedBy     *SkippedBy               `json:"skipped_by,omitempty"`
	Components    *CrucibleScoreComponents `json:"components,omitempty"`
}

// ScoreTotal is the engine-produced aggregate across windows.
// Filled by fitness.AggregateScoreTotal — never written by strategy code,
// which is why RawEvaluateResult deliberately omits this field.
type ScoreTotal struct {
	Fatal              bool     `json:"fatal"`
	Value              *float64 `json:"value,omitempty"`
	Reason             *string  `json:"reason,omitempty"`
	ScoreRaw           *float64 `json:"score_raw,omitempty"`
	ConsistencyPenalty *float64 `json:"consistency_penalty,omitempty"`
}

// ReproducibilityMetadata is the audit trail attached to every Challenger.
type ReproducibilityMetadata struct {
	EpochSeed          int64  `json:"epoch_seed"`
	DataVersion        string `json:"data_version"`
	EngineVersion      string `json:"engine_version"`
	StrategyVersion    string `json:"strategy_version"`
	SchemaVersion      string `json:"schema_version"`
	FitnessVersion     string `json:"fitness_version"`
	FingerprintVersion string `json:"fingerprint_version"`

	// HardwareSignature: "{GOOS}/{GOARCH}/{CPU model}".
	// Records cross-hardware reproducibility boundary; not a hard constraint.
	HardwareSignature string `json:"hardware_signature"`
	GoVersion         string `json:"go_version"`
	BuildID           string `json:"build_id"`

	// PlanHash: lower-hex SHA256 of EvaluablePlan canonical-JSON encoding.
	PlanHash string `json:"plan_hash"`

	// BarsHash: lower-hex SHA256 over the OHLCV+OpenTime canonical JSON
	// of every bar (including warmup) used this Epoch. v3 P04:
	// Bar.IsGap/GapType are metadata and DO NOT enter this hash; gap-
	// detection algorithm changes must not invalidate bars_hash.
	// The serialization contract is defined at the top of
	// internal/quant/canonical_json.go and is frozen.
	BarsHash string `json:"bars_hash"`
}

// GAConfigSnapshot is the effective config captured at task creation.
// v3 P03: friction fields hold EFFECTIVE values. TestMode=true forces
// TakerFeeBPS=0 and SlippageBPS=0 before write. User's original request
// values live on EvolutionTask.RequestedTakerFeeBPS / RequestedSlippageBPS
// for audit, never in this snapshot — that contradiction is what P03 fixes.
type GAConfigSnapshot struct {
	StrategyID           string    `json:"strategy_id"`
	Pair                 string    `json:"pair"`
	PopSize              int       `json:"pop_size"`
	MaxGenerations       int       `json:"max_generations"`
	EliteRatio           float64   `json:"elite_ratio"`
	FatalMDD             float64   `json:"fatal_mdd"`
	TakerFeeBPS          float64   `json:"taker_fee_bps"`
	SlippageBPS          float64   `json:"slippage_bps"`
	SpawnMode            SpawnMode `json:"spawn_mode"`
	TestMode             bool      `json:"test_mode"`
	OosDays              *int      `json:"oos_days,omitempty"`
	FatalAuditSampleRate *float64  `json:"fatal_audit_sample_rate,omitempty"`
}

// ChampionGenePayload wraps a strategy-private gene encoding.
// Engine code MUST NOT inspect Payload — pass it to DecodeElite.
// During the prototype phase Encoding must equal GeneEncodingJSON.
type ChampionGenePayload struct {
	Encoding string          `json:"encoding"`
	Payload  json.RawMessage `json:"payload"`
}

// SpawnPointPayload is the Epoch-level frozen exogenous state.
type SpawnPointPayload struct {
	SpawnMode     SpawnMode       `json:"spawn_mode"`
	CapitalPolicy *string         `json:"capital_policy,omitempty"`
	RiskBounds    json.RawMessage `json:"risk_bounds,omitempty"`
	Meta          json.RawMessage `json:"meta,omitempty"`
}

// ResultCore is the core layer of a ChallengerResultPackage.
type ResultCore struct {
	StrategyID              string                  `json:"strategy_id"`
	ChampionGene            ChampionGenePayload     `json:"champion_gene"`
	SpawnPoint              SpawnPointPayload       `json:"spawn_point"`
	ReproducibilityMetadata ReproducibilityMetadata `json:"reproducibility_metadata"`
	GAConfig                GAConfigSnapshot        `json:"ga_config"`
	SchemaVersion           string                  `json:"schema_version"`
	FitnessVersion          string                  `json:"fitness_version"`
	FingerprintVersion      string                  `json:"fingerprint_version"`
}

// FrictionActual records the friction parameters that were actually in
// effect during evaluation. With v3 P03, this equals
// GAConfigSnapshot.{TakerFeeBPS,SlippageBPS}.
type FrictionActual struct {
	TakerFeeBPS float64  `json:"taker_fee_bps"`
	SlippageBPS float64  `json:"slippage_bps"`
	MakerFeeBPS *float64 `json:"maker_fee_bps,omitempty"`
	SpreadBPS   *float64 `json:"spread_bps,omitempty"`
}

// GapStats summarizes K-line gaps encountered during evaluation.
type GapStats struct {
	TotalGapMinutes   *float64 `json:"total_gap_minutes,omitempty"`
	LongestGapMinutes *float64 `json:"longest_gap_minutes,omitempty"`
	GapCount          *int     `json:"gap_count,omitempty"`
}

// SharpeStats bundles the four DSR inputs derived from one
// challenger's return series. HorizonT mirrors len(returns) for
// audit symmetry — callers should never pass returns with stale
// lengths.
//
// Lives in resultpkg (not verification) because resultpkg is the
// wire-types floor: quant/domain/strategy all depend on it, and
// verification depends on quant. Putting SharpeStats here keeps
// the type accessible to RawEvaluateResult without cycling
// resultpkg → verification → quant → resultpkg.
//
// Sharpe is per-bar (not annualised); annualisation is a display
// concern. The SharpeBank table records the raw per-bar number so
// cross-bar-interval comparisons stay honest.
type SharpeStats struct {
	ObservedSharpe float64
	Skew           float64
	ExcessKurt     float64
	HorizonT       int
}

// RawEvaluateResult is the strategy/Adapter evaluation output (v3 P01).
//
// ⚠️ DELIBERATELY omits ScoreTotal. The type system enforces that the
// strategy layer cannot write aggregate scores. fitness.AggregateScoreTotal
// (engine-only) takes a *RawEvaluateResult and emits a ScoreTotal, which
// the engine then composes into EvaluationLayer.
//
// LongestWindowStats is populated by the strategy when at least one
// non-Fatal window completed. The pointer carries the SharpeStats of
// the longest non-Fatal window in the cascade (typically 10y, but
// 5y/2y/6m if longer windows were skipped). nil ⇒ no usable stats
// (all windows Fatal, or strategy doesn't emit returns). The SaaS
// Epoch service feeds this into SharpeBank.Add and ComputeDSR.
type RawEvaluateResult struct {
	Windows            []CrucibleResult `json:"windows"`
	FrictionActual     FrictionActual   `json:"friction_actual"`
	BarsEvaluated      int              `json:"bars_evaluated"`
	LongestWindowStats *SharpeStats     `json:"longest_window_stats,omitempty"`
	// LongestWindowReturns carries the per-bar log-return series of the
	// same longest non-Fatal window as LongestWindowStats — but ONLY when
	// the plan set CaptureReturns (the post-epoch SBB stress re-run). nil
	// during the GA loop. json:"-": this never serializes (RawEvaluateResult
	// is the internal strategy→engine handoff, not a wire type; only its
	// .Windows reach the persisted package / trace), so it cannot bloat the
	// trace table or move any hash.
	LongestWindowReturns []float64 `json:"-"`
}

// EvaluationLayer is the engine-assembled evaluation layer of the result
// package (v3 P01: renamed from EvaluationResult).
type EvaluationLayer struct {
	WindowScores          []CrucibleResult `json:"window_scores"`
	ScoreTotal            ScoreTotal       `json:"score_total"`
	AlphaBreakdown        json.RawMessage  `json:"alpha_breakdown,omitempty"`
	FrictionActual        FrictionActual   `json:"friction_actual"`
	GapsEncounteredInEval *GapStats        `json:"gaps_encountered_in_eval,omitempty"`
}

// AlphaBreakdownVersionV1 tags the [INVENTED v1] ISAlphaBreakdown shape.
const AlphaBreakdownVersionV1 = "alpha-v1"

// WindowAlpha is one in-sample crucible window's annualized strategy-vs-DCA
// excess return — the same alpha measure RunOOS reports (verification/oos.go),
// computed per IS window. All rates are annualized; alpha_*_ann = strat_ann -
// dca_*_ann against the dual (monthly/weekly) DCA baselines.
//
// [INVENTED v1] This is serialized into EvaluationLayer.AlphaBreakdown
// (a json.RawMessage) and is NOT part of the frozen v5.3.3 schema. A3 fills
// it forward-only as diagnostic data; nothing consumes it yet, so the shape
// may still evolve until a reader pins it (hence the Version tag).
type WindowAlpha struct {
	Window          WindowName `json:"window"`
	StratAnn        float64    `json:"strat_ann"`
	DCAMonthlyAnn   float64    `json:"dca_monthly_ann"`
	DCAWeeklyAnn    float64    `json:"dca_weekly_ann"`
	AlphaMonthlyAnn float64    `json:"alpha_monthly_ann"`
	AlphaWeeklyAnn  float64    `json:"alpha_weekly_ann"`
}

// ISAlphaBreakdown wraps the per-window IS alpha series. Windows that were
// Fatal, cascade-skipped, or degenerate carry no strat return and are absent
// from Windows (an all-Fatal challenger yields an empty slice).
type ISAlphaBreakdown struct {
	Version string        `json:"version"`
	Windows []WindowAlpha `json:"windows"`
}

// OOSResult is the Anchored Holdout outcome.
type OOSResult struct {
	Status          VerificationStatus `json:"status"`
	OOSAlphaMonthly *float64           `json:"oos_alpha_monthly,omitempty"`
	OOSAlphaWeekly  *float64           `json:"oos_alpha_weekly,omitempty"`
	DecisionColor   *DecisionColor     `json:"decision_color,omitempty"`
	Notes           *string            `json:"notes,omitempty"`
}

// ReviewSummary is the reproducibility-replay verdict produced by
// verification.RunReview (backlog A1). Nil when no replay was attempted.
//
//	Status:    ok       — rebuilt plan/bars hashes match the recorded
//	                       metadata AND the replayed fingerprint + IS
//	                       ScoreTotal reproduce the persisted package.
//	           mismatch — any of those gates disagreed; Notes carries the
//	                       recorded-vs-replayed detail. An audit-integrity
//	                       failure, not a strategy-performance verdict.
//	DataScope: which bars the replay covered, e.g. "is-windows". The
//	           full-history dimension is deferred (backlog A1/B).
type ReviewSummary struct {
	Status    VerificationStatus `json:"status"`
	Notes     *string            `json:"notes,omitempty"`
	DataScope *string            `json:"data_scope,omitempty"`
}

// VerificationLayer is the offline-verification layer of the result package.
type VerificationLayer struct {
	OOSResult     OOSResult       `json:"oos_result"`
	ReviewSummary *ReviewSummary  `json:"review_summary,omitempty"`
	DSRSummary    json.RawMessage `json:"dsr_summary,omitempty"`
	StressSummary json.RawMessage `json:"stress_summary,omitempty"`
}

// AuditSampleSummary is one row of the 5% Fatal-audit sample set.
type AuditSampleSummary struct {
	SampleID     string           `json:"sample_id"`
	ScoreTotal   ScoreTotal       `json:"score_total"`
	WindowScores []CrucibleResult `json:"window_scores,omitempty"`
	Notes        *string          `json:"notes,omitempty"`
}

// DiagnosticsLayer is the engine-side diagnostics layer of the result
// package. All fields are optional; emit only what's measured.
type DiagnosticsLayer struct {
	MutationRampLog    json.RawMessage      `json:"mutation_ramp_log,omitempty"`
	DiversityRescueLog json.RawMessage      `json:"diversity_rescue_log,omitempty"`
	ClampModifications json.RawMessage      `json:"clamp_modifications,omitempty"`
	CrossoverFallback  json.RawMessage      `json:"crossover_fallback,omitempty"`
	TurnoverMetrics    json.RawMessage      `json:"turnover_metrics,omitempty"`
	FatalAuditSamples  []AuditSampleSummary `json:"fatal_audit_samples,omitempty"`
}

// PromoteLayer records the human-review outcome.
// v3 P02: DecisionStatus enum approved → promoted.
// Champion retirement is NOT modeled here — see champion_history.
type PromoteLayer struct {
	DecisionStatus DecisionStatus `json:"decision_status"`
	DecisionNote   *string        `json:"decision_note,omitempty"`
	ReviewedAtTS   *int64         `json:"reviewed_at_ts,omitempty"`
	ReviewedBy     *string        `json:"reviewed_by,omitempty"`
}

// ChallengerResultPackage is the five-layer frozen result envelope.
// Persisted as a JSON blob in challenger_result_packages.full_package_json.
type ChallengerResultPackage struct {
	Core         ResultCore        `json:"core"`
	Evaluation   EvaluationLayer   `json:"evaluation"`
	Verification VerificationLayer `json:"verification"`
	Diagnostics  DiagnosticsLayer  `json:"diagnostics"`
	Promote      PromoteLayer      `json:"promote"`
}
