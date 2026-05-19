// Package store holds GORM models, the DB connection bootstrap, and the
// Redis client. Models split into three tiers:
//
//   - Well-specified (struct-doc/coding-plan): EvolutionTask, GeneRecord,
//     KLine, KLineGap, SharpeBank, ChampionHistory.
//   - Placeholder ([INVENTED v1 — needs architect review]): User,
//     StrategyTemplate, StrategyInstance, PortfolioState, RuntimeState,
//     SpotLot, TradeRecord, SpotExecution, AuditLog. The Phase 2 prompt
//     lists these by name but doesn't enumerate fields; minimal-but-useful
//     defaults below, flagged for later review.
//
// All models use uint primary keys via gorm.Model unless a domain ID
// (TaskID, ChallengerID, Symbol+Interval+OpenTime) is more natural.
package store

import (
	"time"

	"gorm.io/gorm"

	"quantlab/internal/resultpkg"
)

// ===================================================================
// Tier 1: Well-specified models
// ===================================================================

// EvolutionTask is one human-initiated evolution run.
//
// v3.2 M16: RequestedTakerFeeBPS / RequestedSlippageBPS record the user's
// original friction intent (audit only). The EFFECTIVE friction values used
// during evaluation live in challenger_result_packages.full_package_json
// (GAConfigSnapshot.TakerFeeBPS/SlippageBPS).
type EvolutionTask struct {
	gorm.Model

	TaskID            string               `gorm:"type:varchar(64);uniqueIndex" json:"task_id"`
	StrategyID        string               `gorm:"type:varchar(64);index"       json:"strategy_id"`
	Pair              string               `gorm:"type:varchar(32);index"       json:"pair"`
	Interval          string               `gorm:"type:varchar(8);index"        json:"interval"`
	Status            resultpkg.TaskStatus `gorm:"type:varchar(16);index"       json:"status"`
	CurrentGeneration int                  `json:"current_generation"`

	// Original user intent (NOT the effective values used in evaluation).
	RequestedTakerFeeBPS float64 `gorm:"column:requested_taker_fee_bps" json:"requested_taker_fee_bps"`
	RequestedSlippageBPS float64 `gorm:"column:requested_slippage_bps" json:"requested_slippage_bps"`

	// User's TestMode request. When true, the engine forces effective
	// friction to 0 in GAConfigSnapshot; resulting challengers cannot
	// be Promoted (enforced in api/handlers, not GORM).
	TestMode bool `json:"test_mode"`

	SpawnMode            resultpkg.SpawnMode `gorm:"type:varchar(16)"       json:"spawn_mode"`
	OosDays              *int                `json:"oos_days,omitempty"`
	FatalAuditSampleRate *float64            `json:"fatal_audit_sample_rate,omitempty"`

	EpochSeed     int64      `json:"epoch_seed"`
	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	FailureReason *string    `gorm:"type:text" json:"failure_reason,omitempty"`

	// ChallengerID + BestScore are populated by MarkSucceeded so the
	// HTTP status response can answer "what did this task produce?"
	// without a secondary join into gene_records. nil while running.
	ChallengerID *string  `gorm:"type:varchar(64);index" json:"challenger_id,omitempty"`
	BestScore    *float64 `json:"best_score,omitempty"`
}

// GeneRecord is the relational mirror of a ChallengerResultPackage,
// suitable for indexing/filtering. Full JSON blob lives in FullPackageJSON.
//
// v3.2 M16: TakerFeeBPS/SlippageBPS hold EFFECTIVE values; "Actual" fields
// from v3.1 are deleted as redundant.
type GeneRecord struct {
	gorm.Model

	ChallengerID string `gorm:"type:varchar(64);uniqueIndex" json:"challenger_id"`
	StrategyID   string `gorm:"type:varchar(64);index"       json:"strategy_id"`
	Pair         string `gorm:"type:varchar(32);index"       json:"pair"`

	// Aggregate scores (decomposed from result package for fast queries).
	ScoreTotal         *float64 `json:"score_total,omitempty"`
	ScoreRaw           *float64 `json:"score_raw,omitempty"`
	ConsistencyPenalty *float64 `json:"consistency_penalty,omitempty"`
	MaxDrawdown        *float64 `json:"max_drawdown,omitempty"`

	// Per-window JSON snippets (denormalized from EvaluationLayer).
	WindowScoresJSON       []byte `gorm:"type:jsonb" json:"window_scores_json,omitempty"`
	WindowAlphaMonthlyJSON []byte `gorm:"type:jsonb" json:"window_alpha_monthly_json,omitempty"`
	WindowAlphaWeeklyJSON  []byte `gorm:"type:jsonb" json:"window_alpha_weekly_json,omitempty"`

	OosAlphaMonthly *float64 `json:"oos_alpha_monthly,omitempty"`
	OosAlphaWeekly  *float64 `json:"oos_alpha_weekly,omitempty"`

	DSR          *float64 `json:"dsr,omitempty"`
	DSRTrialsN   *int     `json:"dsr_trials_n,omitempty"`
	DSRTrialsVar *float64 `json:"dsr_trials_var,omitempty"`

	EpochSeed       int64  `json:"epoch_seed"`
	DataVersion     string `gorm:"type:varchar(64)"  json:"data_version"`
	EngineVersion   string `gorm:"type:varchar(64)"  json:"engine_version"`
	StrategyVersion string `gorm:"type:varchar(64)"  json:"strategy_version"`

	SchemaVersion      string `gorm:"type:varchar(16)" json:"schema_version"`
	FitnessVersion     string `gorm:"type:varchar(32)" json:"fitness_version"`
	FingerprintVersion string `gorm:"type:varchar(16)" json:"fingerprint_version"`

	HardwareSignature string `gorm:"type:varchar(128)" json:"hardware_signature"`
	GoVersion         string `gorm:"type:varchar(32)"  json:"go_version"`
	BuildID           string `gorm:"type:varchar(64)"  json:"build_id"`

	PlanHash string `gorm:"type:varchar(64);index" json:"plan_hash"`
	BarsHash string `gorm:"type:varchar(64);index" json:"bars_hash"`

	// Effective friction (v3.2 M16). For TestMode=true challengers, both are 0.
	TakerFeeBPS float64 `gorm:"column:taker_fee_bps" json:"taker_fee_bps"`
	SlippageBPS float64 `gorm:"column:slippage_bps" json:"slippage_bps"`
	TestMode    bool    `json:"test_mode"`

	SbbBlockLength *int `json:"sbb_block_length,omitempty"`

	// v3.2 M15: DecisionStatus mirrors PromoteLayer (pending/promoted/rejected).
	// "retired" is NEVER stored here — see ChampionHistory.RetiredAt.
	DecisionStatus resultpkg.DecisionStatus `gorm:"type:varchar(16);index" json:"decision_status"`
	DecisionNote   *string                  `gorm:"type:text"              json:"decision_note,omitempty"`
	ReviewedAtTS   *int64                   `json:"reviewed_at_ts,omitempty"`
	ReviewedBy     *string                  `gorm:"type:varchar(64)"       json:"reviewed_by,omitempty"`

	// Serialized ChallengerResultPackage; source of truth for replay.
	FullPackageJSON []byte `gorm:"type:jsonb" json:"full_package_json"`
}

// KLine is one OHLCV bar. Table is registered as a TimescaleDB hypertable
// on open_time with chunk_time_interval = 7 days (see db.go).
type KLine struct {
	Symbol      string  `gorm:"type:varchar(16);primaryKey;index" json:"symbol"`
	Interval    string  `gorm:"type:varchar(8);primaryKey;index"  json:"interval"`
	OpenTime    int64   `gorm:"primaryKey"                        json:"open_time"`
	Open        float64 `json:"open"`
	High        float64 `json:"high"`
	Low         float64 `json:"low"`
	Close       float64 `json:"close"`
	Volume      float64 `json:"volume"`
	QuoteVolume float64 `json:"quote_volume"`
	NumTrades   int32   `json:"num_trades"`
	Source      string  `gorm:"type:varchar(16);default:'binance.vision'" json:"source"`
}

// TableName fixes the table name so GORM doesn't pluralize it weirdly.
func (KLine) TableName() string { return "klines" }

// KLineGap records a missing-bar interval detected by the datafeeder.
// EvaluablePlan construction must consult this table; never silently fill.
type KLineGap struct {
	gorm.Model

	Symbol     string    `gorm:"type:varchar(16);index"        json:"symbol"`
	Interval   string    `gorm:"type:varchar(8);index"         json:"interval"`
	GapStartMs int64     `gorm:"index"                         json:"gap_start_ms"`
	GapEndMs   int64     `gorm:"index"                         json:"gap_end_ms"`
	DetectedAt time.Time `json:"detected_at"`
}

func (KLineGap) TableName() string { return "kline_gaps" }

// SharpeBank is the cross-Epoch DSR accumulator.
type SharpeBank struct {
	gorm.Model

	StrategyID       string  `gorm:"type:varchar(64);index:idx_strategy_pair,priority:1" json:"strategy_id"`
	PairID           string  `gorm:"type:varchar(32);index:idx_strategy_pair,priority:2" json:"pair_id"`
	ChallengerID     string  `gorm:"type:varchar(64);index"                              json:"challenger_id"`
	ObservedSharpe   float64 `json:"observed_sharpe"`
	BacktestHorizonT int     `json:"backtest_horizon_t"`
	Skew             float64 `json:"skew"`
	Kurtosis         float64 `json:"kurtosis"`

	SpawnMode                  resultpkg.SpawnMode `gorm:"type:varchar(16)" json:"spawn_mode"`
	FingerprintDistanceToParent *float64            `json:"fingerprint_distance_to_parent,omitempty"`
}

// ChampionHistory tracks Champion lineage across the Promote/Retire
// lifecycle. "retired" status lives here, never on GeneRecord.
type ChampionHistory struct {
	gorm.Model

	StrategyID   string     `gorm:"type:varchar(64);index" json:"strategy_id"`
	Pair         string     `gorm:"type:varchar(32);index" json:"pair"`
	ChallengerID string     `gorm:"type:varchar(64);index" json:"challenger_id"`
	PromotedAt   time.Time  `json:"promoted_at"`
	RetiredAt    *time.Time `json:"retired_at,omitempty"`
	RetiredBy    *string    `gorm:"type:varchar(64)" json:"retired_by,omitempty"`
	RetireNote   *string    `gorm:"type:text"        json:"retire_note,omitempty"`
}

// ===================================================================
// Tier 2: Placeholder models [INVENTED v1 — needs architect review]
// ===================================================================
// The Phase 2 prompt lists these tables by name without enumerating
// fields. Below are minimal defaults to make AutoMigrate and the rest
// of Phase 2 (config wiring, JWT, server bootstrap) compile and run.
// Field sets must be reviewed and frozen before Phase 6/7 wires them
// into the live SaaS lifecycle.

// User is the human / API caller. [INVENTED v1]
type User struct {
	gorm.Model

	Email        string `gorm:"type:varchar(255);uniqueIndex" json:"email"`
	PasswordHash string `gorm:"type:varchar(255)"             json:"-"`
	Role         string `gorm:"type:varchar(32);index"        json:"role"` // admin / operator / viewer
	DisplayName  string `gorm:"type:varchar(128)"             json:"display_name"`
}

// StrategyTemplate is a registered EvolvableStrategy implementation. [INVENTED v1]
type StrategyTemplate struct {
	gorm.Model

	StrategyID  string `gorm:"type:varchar(64);uniqueIndex" json:"strategy_id"`
	DisplayName string `gorm:"type:varchar(128)"            json:"display_name"`
	Version     string `gorm:"type:varchar(32)"             json:"version"`
	Description string `gorm:"type:text"                    json:"description"`
}

// StrategyInstance is a (template, pair, account) deployment. [INVENTED v1]
type StrategyInstance struct {
	gorm.Model

	InstanceID    string  `gorm:"type:varchar(64);uniqueIndex"   json:"instance_id"`
	StrategyID    string  `gorm:"type:varchar(64);index"         json:"strategy_id"`
	Pair          string  `gorm:"type:varchar(32);index"         json:"pair"`
	AccountID     string  `gorm:"type:varchar(64);index"         json:"account_id"`
	ActiveChampID *string `gorm:"type:varchar(64)"               json:"active_champion_id,omitempty"`
	Status        string  `gorm:"type:varchar(16);default:'idle'" json:"status"` // idle / live / paused
}

// PortfolioState is the asset three-state snapshot at a given NowMs. [INVENTED v1]
type PortfolioState struct {
	gorm.Model

	InstanceID    string  `gorm:"type:varchar(64);index" json:"instance_id"`
	NowMs         int64   `gorm:"index"                  json:"now_ms"`
	DeadBTC       float64 `json:"dead_btc"`
	FloatBTC      float64 `json:"float_btc"`
	ColdSealedBTC float64 `json:"cold_sealed_btc"`
	USDT          float64 `json:"usdt"`
}

// RuntimeState is the strategy-private state blob, persisted as JSON
// between cron ticks. [INVENTED v1]
type RuntimeState struct {
	gorm.Model

	InstanceID string `gorm:"type:varchar(64);uniqueIndex" json:"instance_id"`
	NowMs      int64  `gorm:"index"                        json:"now_ms"`
	StateJSON  []byte `gorm:"type:jsonb"                   json:"state_json"`
}

// SpotLot is a long-lived position lot. [INVENTED v1]
type SpotLot struct {
	gorm.Model

	InstanceID string  `gorm:"type:varchar(64);index"         json:"instance_id"`
	Symbol     string  `gorm:"type:varchar(16);index"         json:"symbol"`
	OpenMs     int64   `json:"open_ms"`
	CloseMs    *int64  `json:"close_ms,omitempty"`
	Quantity   float64 `json:"quantity"`
	EntryPrice float64 `json:"entry_price"`
	Kind       string  `gorm:"type:varchar(16)"               json:"kind"` // macro / micro / cold
}

// TradeRecord is the SaaS-side record of an order intent and its outcome. [INVENTED v1]
type TradeRecord struct {
	gorm.Model

	InstanceID    string  `gorm:"type:varchar(64);index"        json:"instance_id"`
	ClientOrderID string  `gorm:"type:varchar(64);uniqueIndex"  json:"client_order_id"`
	Symbol        string  `gorm:"type:varchar(16);index"        json:"symbol"`
	Side          string  `gorm:"type:varchar(8)"               json:"side"`
	OrderType     string  `gorm:"type:varchar(16)"              json:"order_type"`
	QuantityUSD   float64 `json:"quantity_usd"`
	LimitPrice    *float64 `json:"limit_price,omitempty"`
	NowMsAtSaaS   int64   `json:"now_ms_at_saas"`
	Status        string  `gorm:"type:varchar(16);index"        json:"status"` // pending / acked / filled / cancelled / rejected
}

// SpotExecution is an exchange-side fill reported by the Agent. [INVENTED v1]
type SpotExecution struct {
	gorm.Model

	TradeRecordID    uint    `gorm:"index" json:"trade_record_id"`
	ExchangeOrderID  string  `gorm:"type:varchar(64);index" json:"exchange_order_id"`
	FillQuantity     float64 `json:"fill_quantity"`
	FillPrice        float64 `json:"fill_price"`
	FillFeeAsset     string  `gorm:"type:varchar(16)" json:"fill_fee_asset"`
	FillFeeAmount    float64 `json:"fill_fee_amount"`
	FilledAtExchangeMs int64 `json:"filled_at_exchange_ms"`
}

// AuditLog is a structured event log used for human/agent action trails. [INVENTED v1]
type AuditLog struct {
	gorm.Model

	Actor    string `gorm:"type:varchar(64);index" json:"actor"`
	Action   string `gorm:"type:varchar(64);index" json:"action"`
	Subject  string `gorm:"type:varchar(128);index" json:"subject"`
	NowMs    int64  `json:"now_ms"`
	DataJSON []byte `gorm:"type:jsonb" json:"data_json"`
}

// AllModels returns every GORM model for AutoMigrate.
// Keep in sync when adding new tables.
func AllModels() []interface{} {
	return []interface{}{
		// Tier 1
		&EvolutionTask{},
		&GeneRecord{},
		&KLine{},
		&KLineGap{},
		&SharpeBank{},
		&ChampionHistory{},
		// Tier 2 [INVENTED v1]
		&User{},
		&StrategyTemplate{},
		&StrategyInstance{},
		&PortfolioState{},
		&RuntimeState{},
		&SpotLot{},
		&TradeRecord{},
		&SpotExecution{},
		&AuditLog{},
	}
}
