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
