// apiClient: a thin fetch wrapper over the same-origin /api/v1 surface.
// Injects the bearer token, parses the {error} convention, and routes
// 401s through a global handler so an expired/invalid token kicks the
// user back to login from anywhere.

const BASE = '/api/v1'

export class ApiError extends Error {
  status: number
  constructor(status: number, message: string) {
    super(message)
    this.status = status
    this.name = 'ApiError'
  }
}

// AuthProvider registers logout here; apiFetch calls it on any 401 so
// token expiry is handled in one place rather than per-query.
let onUnauthorized: (() => void) | null = null
export function setUnauthorizedHandler(fn: (() => void) | null): void {
  onUnauthorized = fn
}

export interface ApiOpts {
  method?: string
  body?: unknown
  token?: string | null
}

export async function apiFetch<T>(path: string, opts: ApiOpts = {}): Promise<T> {
  const headers: Record<string, string> = {}
  if (opts.body !== undefined) headers['Content-Type'] = 'application/json'
  if (opts.token) headers['Authorization'] = `Bearer ${opts.token}`

  const res = await fetch(`${BASE}${path}`, {
    method: opts.method ?? 'GET',
    headers,
    body: opts.body !== undefined ? JSON.stringify(opts.body) : undefined,
  })

  if (res.status === 401) onUnauthorized?.()

  if (!res.ok) {
    let msg = `HTTP ${res.status}`
    try {
      const j = (await res.json()) as { error?: string }
      if (j?.error) msg = j.error
    } catch {
      // Non-JSON error body — keep the status-derived message.
    }
    throw new ApiError(res.status, msg)
  }

  if (res.status === 204) return undefined as T
  return (await res.json()) as T
}
