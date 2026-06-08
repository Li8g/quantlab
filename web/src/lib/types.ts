// API boundary types — mirror the Go response structs in
// internal/api/types.go + handlers_auth.go. Timestamps are epoch
// milliseconds (the _ms suffix convention).

export interface LoginResponse {
  token: string
  role: string
  expires_at: number // unix ms
}

// One row of GET /champions/history.
export interface ChampionHistoryEntry {
  id: number
  strategy_id: string
  pair: string
  challenger_id: string
  promoted_at_ms: number
  retired_at_ms?: number
  retired_by?: string
  retire_note?: string
}

export interface ListChampionHistoryResponse {
  items: ChampionHistoryEntry[]
  count: number
}

export type TaskStatus =
  | 'queued'
  | 'running'
  | 'succeeded'
  | 'failed'
  | 'cancelled'

// One row of GET /evolution/tasks. Note: no challenger_id here — the
// winner is on the per-task status response below (drill in to get it).
export interface EvolutionTaskSummary {
  task_id: string
  strategy_id: string
  pair: string
  interval: string
  status: TaskStatus
  created_at_ms: number
}

export interface ListTasksResponse {
  items: EvolutionTaskSummary[]
  count: number
}

// GET /evolution/tasks/:task_id — carries the winning challenger_id once
// the task succeeds.
export interface EvolutionTaskStatusResponse {
  task_id: string
  status: TaskStatus
  current_generation: number
  best_score?: number
  challenger_id?: string
  failure_reason?: string
}

export type DecisionStatus = 'pending' | 'promoted' | 'rejected'

// GET /challengers/:id — lifted-column summary.
export interface ChallengerSummary {
  challenger_id: string
  strategy_id: string
  pair: string
  score_total?: number
  score_raw?: number
  consistency_penalty?: number
  decision_status: DecisionStatus
  plan_hash: string
  bars_hash: string
  test_mode: boolean
  dsr?: number
  promoted_at_ms?: number
  retired_at_ms?: number
}

export type WindowName = '6m' | '2y' | '5y' | '10y'

// SliceScore three-state: normal (value set), self-fatal (fatal=true,
// value null), or cascade-skipped (skipped_by set on the WindowScore).
export interface SliceScore {
  fatal: boolean
  value?: number
}

export interface WindowScore {
  window: WindowName
  score: SliceScore
  fatal_reason?: string
  fatal_mdd_value?: number
  bars_evaluated: number
  skipped_by?: string
}

export interface FrictionActual {
  taker_fee_bps: number
  slippage_bps: number
}

export type VerificationStatus = 'ok' | 'insufficient_data' | 'failed' | 'not_run'
export type DecisionColor = 'green' | 'yellow' | 'red' | 'gray'

export interface OOSResult {
  status: VerificationStatus
  oos_alpha_monthly?: number
  oos_alpha_weekly?: number
  decision_color?: DecisionColor
  notes?: string
}

// GET /challengers/:id/package — only the layers F1 renders are typed.
export interface ChallengerPackage {
  evaluation: {
    window_scores: WindowScore[]
    friction_actual: FrictionActual
  }
  verification: {
    oos_result: OOSResult
  }
}

// ---- F2 live monitor (mirrors internal/api/live_handlers.go) ----

export type InstanceStatus = 'idle' | 'live' | 'paused' | 'retired'

// One row of GET /instances and the embedded instance on /live.
export interface InstanceResponse {
  instance_id: string
  strategy_id: string
  pair: string
  account_id: string
  owner_user_id: number
  status: InstanceStatus
  active_champion_id?: string
  last_tick_wall_time_ms?: number
}

export interface InstanceListResponse {
  items: InstanceResponse[]
  count: number
}

// Latest portfolio_states row. equity/mark_price are present only when a
// mark price was available (Tier M); equity = (dead+float)*mark + usdt,
// cold_sealed excluded per the strategy NAV formula.
export interface PortfolioSnapshotView {
  dead_btc: number
  float_btc: number
  cold_sealed_btc: number
  usdt: number
  now_ms: number
  last_processed_bar_time: number
  equity?: number
  mark_price?: number
  mark_price_ms?: number
}

export interface ConnectionHealth {
  connected: boolean
}

// One exchange-side fill, folded onto its parent trade.
export interface SpotExecutionSummary {
  exchange_order_id: string
  fill_quantity: number
  fill_price: number
  fill_fee_asset: string
  fill_fee_amount: number
  filled_at_exchange_ms: number
  actual_slippage_bps: number
}

export interface TradeRecordSummary {
  client_order_id: string
  symbol: string
  side: string
  order_type: string
  quantity_usd: number
  limit_price?: number
  now_ms_at_saas: number
  valid_until_ms: number
  status: string
  created_at_ms: number
  fills?: SpotExecutionSummary[]
}

// One persisted position-drift row (Phase 8 持仓对账), Tier L.
export interface ReconciliationDiscrepancyView {
  asset: string
  expected_amount: number
  actual_amount: number
  diff_amount: number
  drift_bps: number
  reported_at_ms: number
  detected_at_ms: number
}

// One persisted exchange-layer error from a delta_report, Tier L.
export interface AgentErrorView {
  code: string
  message: string
  occurred_at_ms: number
  reported_at_ms: number
}

// Most recent kill_switch for the instance's account (Option 3 step 4).
// Present ⇒ the account is currently frozen: the backend derives this from
// the latest kill/resume event (LatestKillOrResume), so a §5.13 v2 resume
// clears it. This is a real "still frozen?" signal, not just the last kill.
export interface KillStatusView {
  killed_at_ms: number
  actor: string // "user:<id>" | "system"
  reason: string // wire.KillSwitchReason
  operator_user_id?: string
  trigger: string // "manual" | "auto"
}

// GET /instances/:id/live — aggregate snapshot. portfolio/connection are
// omitted when unavailable; recent_trades is always present.
// recent_discrepancies/recent_errors are omitted when the recon
// collaborator is unwired (Tier L nil-skip). kill_status is present only
// while the account is currently frozen (cleared by a resume).
export interface InstanceLiveResponse {
  instance: InstanceResponse
  portfolio?: PortfolioSnapshotView
  connection?: ConnectionHealth
  recent_trades: TradeRecordSummary[]
  recent_discrepancies?: ReconciliationDiscrepancyView[]
  recent_errors?: AgentErrorView[]
  kill_status?: KillStatusView
  // ⑤ staleness guard threshold — mirrors data_feed.max_bar_staleness so the
  // age badge uses the same bound as the server-side trading guard.
  max_bar_staleness_ms?: number
}
