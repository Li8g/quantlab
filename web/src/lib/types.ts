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
