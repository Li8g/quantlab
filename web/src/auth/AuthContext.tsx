import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from 'react'
import { apiFetch, setUnauthorizedHandler } from '../lib/api'
import type { LoginResponse } from '../lib/types'

// Sudo-style auth (docs/frontend-promote-retire-v1.md §3): the default
// session is a viewer token. Elevation to admin for promote/retire is a
// separate, short-lived step-up handled by SudoModal (F1) — this context
// holds only the standing session.

export interface AuthState {
  token: string
  role: string
  expiresAt: number // unix ms
  email: string // kept for SudoModal re-login + reviewed_by
}

interface AuthContextValue {
  auth: AuthState | null
  login: (email: string, password: string, role?: string) => Promise<void>
  logout: () => void
}

const STORAGE_KEY = 'quantlab.auth'

// Hydrate from sessionStorage, dropping an already-expired token.
function loadStored(): AuthState | null {
  const raw = sessionStorage.getItem(STORAGE_KEY)
  if (!raw) return null
  try {
    const s = JSON.parse(raw) as AuthState
    if (!s.token || s.expiresAt <= Date.now()) return null
    return s
  } catch {
    return null
  }
}

const AuthContext = createContext<AuthContextValue | null>(null)

export function AuthProvider({ children }: { children: ReactNode }) {
  const [auth, setAuth] = useState<AuthState | null>(loadStored)

  const logout = useCallback(() => {
    sessionStorage.removeItem(STORAGE_KEY)
    setAuth(null)
  }, [])

  const login = useCallback(
    async (email: string, password: string, role = 'viewer') => {
      const res = await apiFetch<LoginResponse>('/auth/login', {
        method: 'POST',
        body: { email, password, role },
      })
      const next: AuthState = {
        token: res.token,
        role: res.role,
        expiresAt: res.expires_at,
        email,
      }
      sessionStorage.setItem(STORAGE_KEY, JSON.stringify(next))
      setAuth(next)
    },
    [],
  )

  // Any 401 from apiFetch (expired/invalid token) drops the session.
  useEffect(() => {
    setUnauthorizedHandler(logout)
    return () => setUnauthorizedHandler(null)
  }, [logout])

  const value = useMemo(() => ({ auth, login, logout }), [auth, login, logout])
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>
}

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
