// Package store holds GORM models, the DB connection bootstrap, and the
// Redis client. Models split into two tiers:
//
//   - Tier 1 (well-specified by struct-doc/coding-plan): EvolutionTask,
//     GeneRecord, KLine, KLineGap, SharpeBank, ChampionHistory.
//   - Tier 2 (live-trading state, frozen per docs/saas-tier2-schema-v1.md
//     2026-05-20): User, StrategyTemplate, StrategyInstance,
//     PortfolioState, RuntimeState, AuditLog. SpotLot, TradeRecord,
//     SpotExecution Group-D fields align with docs/saas-ws-protocol-v1.md
//     §5.8/§5.9/§5.10 (TradeCommand / Ack / OrderUpdate; frozen 2026-05-20).
//
// Tier 1 uses gorm.Model (uint PK + soft-delete) where applicable.
// Tier 2 uses explicit ID + CreatedAt (+ UpdatedAt where applicable)
// per CC1 (no soft-delete anywhere; failure to deactivate uses
// Active bool / Status enum / CloseMs / business time fields).
package store

import (
	"encoding/json"
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

	SpawnMode                   resultpkg.SpawnMode `gorm:"type:varchar(16)" json:"spawn_mode"`
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

// EvaluationTrace is one (gene, score) sample from inside a GA run —
// every individual of every generation, not just the eventual winner.
// Phase 1.5: feeds Optuna-dashboard analytics that need population-scale
// data to be meaningful (fANOVA, parallel coordinates).
//
// Volume: PopSize × MaxGenerations rows per task (typical 200 × 30 ≈
// 6000). Append-only audit; soft-delete inherited from gorm.Model is
// kept for cross-cutting cleanup but never used by the engine path.
// Retention policy is a follow-up — table grows linearly with tasks.
type EvaluationTrace struct {
	gorm.Model

	TaskID        string `gorm:"type:varchar(64);index:idx_task_gen,priority:1" json:"task_id"`
	Generation    int    `gorm:"index:idx_task_gen,priority:2"                  json:"generation"`
	IndividualIdx int    `json:"individual_idx"`

	GeneJSON []byte `gorm:"type:jsonb" json:"gene_json"`

	// Aggregate score. NULL when Fatal (no aggregate is computed).
	ScoreTotal         *float64 `json:"score_total,omitempty"`
	ScoreRaw           *float64 `json:"score_raw,omitempty"`
	ConsistencyPenalty *float64 `json:"consistency_penalty,omitempty"`

	Fatal       bool    `gorm:"index"            json:"fatal"`
	FatalReason *string `gorm:"type:text"        json:"fatal_reason,omitempty"`

	// Compact per-window summary: [{"window":"6m","fatal":false,"value":0.31}, ...].
	// Cheaper than the full CrucibleResult blob — diagnostics/components
	// stripped. Enough for Optuna's intermediate-values view.
	WindowScoresJSON []byte `gorm:"type:jsonb" json:"window_scores_json,omitempty"`

	Fingerprint string `gorm:"type:varchar(64);index" json:"fingerprint"`
}

// ===================================================================
// Tier 2: live-trading state, frozen per docs/saas-tier2-schema-v1.md
// ===================================================================
// All Tier 2 models share these conventions:
//   - No gorm.Model (no soft-delete; explicit ID + CreatedAt + UpdatedAt
//     where applicable). Failure-to-deactivate uses Active bool /
//     Status enum / CloseMs / business time fields. (CC1)
//   - Business IDs use ULID 26-char (CC3); Tier 1 hex stays.
//   - NowMs + CreatedAt dual-time per CC2.

// -------- A. User --------

type UserRole string

const (
	UserRoleAdmin    UserRole = "admin"    // 全权
	UserRoleOperator UserRole = "operator" // 管理自有 Instance；读全部；不能 Promote
	UserRoleViewer   UserRole = "viewer"   // 只读
)

// roleRank orders the three roles for hierarchy comparisons; higher
// number = more privilege.
var roleRank = map[UserRole]int{
	UserRoleViewer:   1,
	UserRoleOperator: 2,
	UserRoleAdmin:    3,
}

// ParseUserRole validates the input string and returns the
// corresponding UserRole. Unknown values return (_, false).
func ParseUserRole(s string) (UserRole, bool) {
	r := UserRole(s)
	_, ok := roleRank[r]
	return r, ok
}

// RoleAtLeast reports whether `have` is at least as privileged as
// `want`. Used by /auth/login to cap the requested role at the user's
// DB role: a request for admin from a viewer-DB-row is rejected. Both
// inputs must be valid UserRole values — unknown roles return false.
func RoleAtLeast(have, want UserRole) bool {
	h, ok := roleRank[have]
	if !ok {
		return false
	}
	w, ok := roleRank[want]
	if !ok {
		return false
	}
	return h >= w
}

// User is the human / API caller (§3.1).
type User struct {
	ID           uint       `gorm:"primaryKey"                          json:"id"`
	UserID       string     `gorm:"type:varchar(32);uniqueIndex"        json:"user_id"` // ULID; URL 暴露字段
	CreatedAt    time.Time  `gorm:"index"                               json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	Email        string     `gorm:"type:varchar(255);uniqueIndex"       json:"email"`
	PasswordHash string     `gorm:"type:varchar(255);not null"          json:"-"`
	Role         UserRole   `gorm:"type:varchar(16);index;not null"     json:"role"`
	DisplayName  string     `gorm:"type:varchar(128)"                   json:"display_name"`
	Active       bool       `gorm:"index;default:true;not null"         json:"active"`
	LastLoginAt  *time.Time `json:"last_login_at,omitempty"`
}

// -------- B. StrategyTemplate + StrategyInstance --------

type InstanceStatus string

const (
	InstanceStatusIdle    InstanceStatus = "idle"    // 已创建未启动
	InstanceStatusLive    InstanceStatus = "live"    // Cron Tick 中
	InstanceStatusPaused  InstanceStatus = "paused"  // 手动暂停（保留状态）
	InstanceStatusRetired InstanceStatus = "retired" // 终态
)

// StrategyTemplate is the DB-side catalog of registered strategies
// (§4.1). epoch.Registry is the code-side SoT; startup syncs Template
// rows from registry.IDs().
type StrategyTemplate struct {
	ID                   uint      `gorm:"primaryKey"                       json:"id"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
	StrategyID           string    `gorm:"type:varchar(64);uniqueIndex"     json:"strategy_id"`
	DisplayName          string    `gorm:"type:varchar(128);not null"       json:"display_name"`
	Version              string    `gorm:"type:varchar(32);not null"        json:"version"`
	Description          string    `gorm:"type:text"                        json:"description"`
	Active               bool      `gorm:"index;default:true;not null"      json:"active"`
	ChromosomeSchemaJSON []byte    `gorm:"type:jsonb"                       json:"chromosome_schema_json,omitempty"`
}

// StrategyInstance is a (User, Strategy, Pair, Account) live deployment
// (§4.2). Partial unique on (owner_user_id, strategy_id, pair, account_id)
// WHERE status != 'retired' is created via raw SQL in db.go.
type StrategyInstance struct {
	ID               uint           `gorm:"primaryKey"                            json:"id"`
	CreatedAt        time.Time      `json:"created_at"`
	UpdatedAt        time.Time      `json:"updated_at"`
	InstanceID       string         `gorm:"type:varchar(32);uniqueIndex"          json:"instance_id"` // ULID
	StrategyID       string         `gorm:"type:varchar(64);index;not null"       json:"strategy_id"`
	Pair             string         `gorm:"type:varchar(32);index;not null"       json:"pair"`
	AccountID        string         `gorm:"type:varchar(64);index;not null"       json:"account_id"` // scoped by OwnerUserID
	OwnerUserID      uint           `gorm:"index;not null"                         json:"owner_user_id"`
	Status           InstanceStatus `gorm:"type:varchar(16);default:'idle';index" json:"status"`
	ActiveChampID    *string        `gorm:"type:varchar(64);index"                 json:"active_champion_id,omitempty"`
	LastTickWallTime *time.Time     `json:"last_tick_wall_time,omitempty"` // wall clock for ops
	// FundedAtMs anchors the genesis funding moment: NULL until the first
	// exchange snapshot seeds the SaaS ledger from real holdings, set once
	// thereafter. While NULL, reconciling the instance's $0 ledger against
	// real balances would false-flag every holding as drift, so funding
	// seeds the ledger first and flips this. See cmd/saas/agentmsg.go
	// fundInstance. [INVENTED v1]
	FundedAtMs *int64 `gorm:"index" json:"funded_at_ms,omitempty"`
}

// -------- C. PortfolioState + RuntimeState --------

// PortfolioState is the asset three-state snapshot at one Tick (§5.1).
// Append-only history: each Tick writes one row (heartbeat + simplicity).
// portfolio_states is registered as a TimescaleDB hypertable on now_ms
// in db.go.
//
// Composite primary key (instance_id, now_ms) — TimescaleDB requires any
// unique index on a hypertable to include the partition column. Same
// pattern as klines, no synthetic id needed (one Tick = one moment).
type PortfolioState struct {
	InstanceID string `gorm:"type:varchar(32);primaryKey"  json:"instance_id"`
	NowMs      int64  `gorm:"primaryKey"                    json:"now_ms"`
	CreatedAt  time.Time

	DeadBTC       float64 `json:"dead_btc"`
	FloatBTC      float64 `json:"float_btc"`
	ColdSealedBTC float64 `json:"cold_sealed_btc"`
	USDT          float64 `json:"usdt"`

	LastProcessedBarTime int64 `json:"last_processed_bar_time"`
}

// RuntimeState is the strategy-private opaque blob (§5.2). Current-state
// only: each Tick UPSERTs the row (one row per InstanceID). StateJSON
// soft limit ≤ 64KB (jsonb 1 page = 8KB; over that → TOAST).
type RuntimeState struct {
	ID         uint            `gorm:"primaryKey"                    json:"id"`
	CreatedAt  time.Time       `json:"created_at"`
	UpdatedAt  time.Time       `json:"updated_at"`
	InstanceID string          `gorm:"type:varchar(32);uniqueIndex"  json:"instance_id"`
	NowMs      int64           `gorm:"not null"                       json:"now_ms"`
	StateJSON  json.RawMessage `gorm:"type:jsonb;not null"            json:"state_json"`
}

// -------- D. SpotLot + TradeRecord + SpotExecution --------
//
// Field set aligned with docs/saas-ws-protocol-v1.md §5.8/§5.9/§5.10
// (frozen 2026-05-20). See appendix B of that doc for the field-by-field
// mapping between GORM columns here and the wire payloads.

type LotKind string

const (
	LotKindMacro LotKind = "macro" // 宏观引擎建立
	LotKindMicro LotKind = "micro" // 微观引擎建立
	LotKindCold  LotKind = "cold"  // 已转 ColdSealedBTC
)

type TradeStatus string

const (
	TradeStatusPending       TradeStatus = "pending"
	TradeStatusAcked         TradeStatus = "acked"
	TradeStatusFilled        TradeStatus = "filled"
	TradeStatusPartialFilled TradeStatus = "partial_filled"
	TradeStatusCancelled     TradeStatus = "cancelled"
	TradeStatusRejected      TradeStatus = "rejected"
)

// SpotLot is a long-lived position lot (§6.1).
// OpenMs/CloseMs取自 entry/close trade 首个 SpotExecution.FilledAtExchangeMs.
type SpotLot struct {
	ID           uint      `gorm:"primaryKey"                      json:"id"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
	LotID        string    `gorm:"type:varchar(32);uniqueIndex"    json:"lot_id"` // ULID
	InstanceID   string    `gorm:"type:varchar(32);index;not null" json:"instance_id"`
	Symbol       string    `gorm:"type:varchar(16);index;not null" json:"symbol"`
	Kind         LotKind   `gorm:"type:varchar(8);index;not null"  json:"kind"`
	OpenMs       int64     `gorm:"not null"                         json:"open_ms"`
	CloseMs      *int64    `json:"close_ms,omitempty"`
	Quantity     float64   `gorm:"not null"                         json:"quantity"`
	EntryPrice   float64   `gorm:"not null"                         json:"entry_price"`
	EntryTradeID string    `gorm:"type:varchar(32);index"           json:"entry_trade_id"` // client_order_id
}

// TradeRecord is the SaaS-side record of an order intent and its outcome
// (§6.2). Most fields mirror OrderIntent / TradeCommand wire formats.
type TradeRecord struct {
	ID            uint        `gorm:"primaryKey"                              json:"id"`
	CreatedAt     time.Time   `json:"created_at"`
	UpdatedAt     time.Time   `json:"updated_at"`
	ClientOrderID string      `gorm:"type:varchar(32);uniqueIndex"            json:"client_order_id"`
	InstanceID    string      `gorm:"type:varchar(32);index;not null"         json:"instance_id"`
	Symbol        string      `gorm:"type:varchar(16);index;not null"         json:"symbol"`
	Side          string      `gorm:"type:varchar(8);not null"                json:"side"`
	OrderType     string      `gorm:"type:varchar(16);not null"               json:"order_type"`
	QuantityUSD   float64     `gorm:"not null"                                 json:"quantity_usd"`
	LimitPrice    *float64    `json:"limit_price,omitempty"`
	NowMsAtSaaS   int64       `gorm:"not null"                                 json:"now_ms_at_saas"`
	ValidUntilMs  int64       `gorm:"not null"                                 json:"valid_until_ms"`
	Status        TradeStatus `gorm:"type:varchar(16);index;default:'pending'" json:"status"`
	LotID         *string     `gorm:"type:varchar(32);index"                   json:"lot_id,omitempty"`
}

// SpotExecution is an exchange-side fill reported by the Agent (§6.3).
// Multiple per TradeRecord for partial fills.
//
// ExchangeOrderID is the exchange's own ID; unique in (account, symbol)
// not globally. Original `index` is fine for single-account prototype;
// multi-account scaling should change to composite unique
// (instance_id, exchange_order_id).
type SpotExecution struct {
	ID                 uint `gorm:"primaryKey"                       json:"id"`
	CreatedAt          time.Time
	ClientOrderID      string  `gorm:"type:varchar(32);index;not null"  json:"client_order_id"` // FK to TradeRecord
	ExchangeOrderID    string  `gorm:"type:varchar(64);index;not null"  json:"exchange_order_id"`
	FillQuantity       float64 `gorm:"not null"                          json:"fill_quantity"`
	FillPrice          float64 `gorm:"not null"                          json:"fill_price"`
	FillFeeAsset       string  `gorm:"type:varchar(16);not null"        json:"fill_fee_asset"`
	FillFeeAmount      float64 `gorm:"not null"                          json:"fill_fee_amount"`
	FilledAtExchangeMs int64   `gorm:"not null"                          json:"filled_at_exchange_ms"`
	ActualSlippageBPS  float64 `json:"actual_slippage_bps"` // Agent-computed
}

// -------- F. AgentToken (added 2026-05-20 for Phase 7 Step 2) --------
//
// Bearer-token persistence for Agent ↔ SaaS WS authentication, per
// docs/saas-ws-protocol-v1.md §4.3. One row per provisioned Agent.
// AgentID is embedded in the token prefix (agt_<AgentID>_<secret>) so
// Verify can target a single row without a full-table bcrypt scan.
//
// TokenHash is bcrypt(secret_only), NOT bcrypt(full_token) — the agent_id
// portion is non-secret routing metadata.
type AgentToken struct {
	ID         uint       `gorm:"primaryKey"                       json:"id"`
	CreatedAt  time.Time  `gorm:"index"                            json:"created_at"`
	UpdatedAt  time.Time  `json:"updated_at"`
	AgentID    string     `gorm:"type:varchar(32);uniqueIndex"     json:"agent_id"`   // ULID
	AccountID  string     `gorm:"type:varchar(32);index;not null"  json:"account_id"` // ULID
	TokenHash  string     `gorm:"type:varchar(60);not null"        json:"-"`          // bcrypt(secret) cost=12
	Label      string     `gorm:"type:varchar(64)"                 json:"label,omitempty"`
	LastSeenAt *time.Time `json:"last_seen_at,omitempty"`
	RevokedAt  *time.Time `gorm:"index"                            json:"revoked_at,omitempty"`
}

// -------- E. AuditLog --------

type AuditAction string

const (
	// 决策
	AuditActionChallengerPromote AuditAction = "challenger.promote"
	AuditActionChampionRetire    AuditAction = "champion.retire"
	// 进化任务
	AuditActionTaskCreate  AuditAction = "task.create"
	AuditActionTaskSucceed AuditAction = "task.succeed"
	AuditActionTaskFail    AuditAction = "task.fail"
	// 实例生命周期
	AuditActionInstanceCreate         AuditAction = "instance.create"
	AuditActionInstanceStart          AuditAction = "instance.start"
	AuditActionInstanceStop           AuditAction = "instance.stop"
	AuditActionInstanceDeployChampion AuditAction = "instance.deploy_champion"
	AuditActionInstanceKill           AuditAction = "instance.kill"
	// Agent
	AuditActionAgentConnect        AuditAction = "agent.connect"
	AuditActionAgentDisconnect     AuditAction = "agent.disconnect"
	AuditActionAgentHeartbeatStale AuditAction = "agent.heartbeat_stale"
	// 用户 / 认证
	AuditActionAuthLogin        AuditAction = "auth.login"
	AuditActionAuthLoginFailure AuditAction = "auth.login_failure"
	AuditActionUserCreate       AuditAction = "user.create"
	AuditActionUserDisable      AuditAction = "user.disable"
	AuditActionUserRoleChange   AuditAction = "user.role_change"
	// 手工
	AuditActionManualPortfolioAdjust AuditAction = "manual.portfolio_adjust"
	AuditActionManualLotClose        AuditAction = "manual.lot_close"
)

// AuditLog is a structured event log used for human/agent action trails
// (§7.1). Insert-only; never deleted.
//
// Actor convention: `user:<user_id ULID>` / `agent:<agent_id>` / `system`.
// Subject is the action's direct target (e.g. `challenger:<id>`); multi-
// subject events put secondaries in DataJSON. See §7.4 for the
// per-Action payload convention.
type AuditLog struct {
	ID        uint            `gorm:"primaryKey"                                 json:"id"`
	CreatedAt time.Time       `gorm:"index"                                      json:"created_at"`
	NowMs     *int64          `json:"now_ms,omitempty"` // v1 阶段所有 19 个 Action 均填 nil
	Actor     string          `gorm:"type:varchar(64);index;not null"            json:"actor"`
	Action    AuditAction     `gorm:"type:varchar(48);index;not null"            json:"action"`
	Subject   string          `gorm:"type:varchar(128);index;not null"           json:"subject"`
	DataJSON  json.RawMessage `gorm:"type:jsonb"                                 json:"data_json,omitempty"`
}

// ReconciliationDiscrepancy is one detected drift between the exchange's
// reported holdings (delta_report.positions — ground truth) and SaaS's own
// portfolio bookkeeping, recorded per asset. Append-only forensic trail:
// the durable, queryable, joinable record behind 持仓对账 — chosen over
// ephemeral logs precisely so incident retrospective survives log rotation
// (Phase 8, Option 2). [INVENTED v1]
type ReconciliationDiscrepancy struct {
	ID             uint      `gorm:"primaryKey"                      json:"id"`
	CreatedAt      time.Time `gorm:"index"                           json:"created_at"`
	AccountID      string    `gorm:"type:varchar(64);index;not null" json:"account_id"`
	InstanceID     string    `gorm:"type:varchar(32);index"          json:"instance_id"`    // resolved when account↔instance is 1:1; "" when account spans many
	Asset          string    `gorm:"type:varchar(16);not null"       json:"asset"`          // "BTC" / "USDT"
	ExpectedAmount float64   `json:"expected_amount"`                                       // SaaS bookkeeping (BTC: dead+float+cold; USDT)
	ActualAmount   float64   `json:"actual_amount"`                                         // exchange free+locked
	DiffAmount     float64   `json:"diff_amount"`                                           // actual - expected
	DriftBps       float64   `json:"drift_bps"`                                             // |diff| / max(|expected|,|actual|,floor) * 1e4
	ReportedAtMs   int64     `gorm:"index"                           json:"reported_at_ms"` // delta_report.reported_at_ms
	DetectedAtMs   int64     `json:"detected_at_ms"`                                        // SaaS wall clock at detection
}

// AgentError is one exchange-layer error the Agent collected since its last
// delta_report (rate limits, partial outages — §5.11 since_last_report.errors).
// Append-only; feeds incident retrospective and the Tier L error stream.
// Distinct from the wire-level `error` message (§5.15). [INVENTED v1]
type AgentError struct {
	ID           uint      `gorm:"primaryKey"                      json:"id"`
	CreatedAt    time.Time `gorm:"index"                           json:"created_at"`
	AccountID    string    `gorm:"type:varchar(64);index;not null" json:"account_id"`
	InstanceID   string    `gorm:"type:varchar(32);index"          json:"instance_id"`
	Code         string    `gorm:"type:varchar(64);index;not null" json:"code"`
	Message      string    `gorm:"type:text"                       json:"message"`
	OccurredAtMs int64     `gorm:"index"                           json:"occurred_at_ms"`
	ReportedAtMs int64     `json:"reported_at_ms"` // delta_report.reported_at_ms that carried it
}

// ImportJob is one async kline-import task — the HTTP-pollable twin of
// the CLI `datafeeder import`, wrapping data.Orchestrator.ImportSymbol in
// the evolution_tasks create→poll model (Phase 9, docs/phase9-data-import-v1.md).
// Status reuses resultpkg.TaskStatus (no parallel enum). A partial unique
// index on (symbol,interval) WHERE status IN (queued,running) enforces
// per-pair mutual exclusion (added in db.go after AutoMigrate — GORM tags
// can't express partial unique).
type ImportJob struct {
	gorm.Model
	JobID    string               `gorm:"type:varchar(64);uniqueIndex" json:"import_job_id"`
	Symbol   string               `gorm:"type:varchar(16);index"       json:"symbol"`
	Interval string               `gorm:"type:varchar(8);index"        json:"interval"`
	StartMs  int64                `json:"start_ms"`
	EndMs    int64                `json:"end_ms"`
	Status   resultpkg.TaskStatus `gorm:"type:varchar(16);index"       json:"status"`

	MonthsDone   int   `json:"months_done"`
	MonthsTotal  int   `json:"months_total"`
	RowsInserted int64 `json:"rows_inserted"`
	GapsDetected int   `json:"gaps_detected"`

	CancelRequested bool   `gorm:"index" json:"cancel_requested"`              // 决策 5: worker checks at month end
	RequestedBy     string `gorm:"type:varchar(64);index" json:"requested_by"` // triggering admin, for audit

	StartedAt     *time.Time `json:"started_at,omitempty"`
	FinishedAt    *time.Time `json:"finished_at,omitempty"`
	FailureReason *string    `gorm:"type:text" json:"failure_reason,omitempty"`
}

func (ImportJob) TableName() string { return "import_jobs" }

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
		&EvaluationTrace{},
		// Tier 2 (frozen 2026-05-20; Group D wire-aligned per
		// docs/saas-ws-protocol-v1.md §5.8/§5.9/§5.10)
		&User{},
		&StrategyTemplate{},
		&StrategyInstance{},
		&PortfolioState{},
		&RuntimeState{},
		&SpotLot{},
		&TradeRecord{},
		&SpotExecution{},
		&AuditLog{},
		// Phase 7 Step 2 — Agent WS bearer-token table (saas-ws-protocol-v1.md §4.3)
		&AgentToken{},
		// Phase 8 — delta_report reconciliation forensic trail (Option 2)
		&ReconciliationDiscrepancy{},
		&AgentError{},
		// Phase 9 — async kline import jobs
		&ImportJob{},
	}
}
