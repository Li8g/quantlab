import { createContext, useContext } from 'react'

// Auth context object + hook, split out of AuthContext.tsx so that file
// exports only the AuthProvider component (react-refresh/only-export-
// components: a file mixing component + non-component exports breaks Fast
// Refresh). AuthProvider sets this context; useAuth reads it.

export interface AuthState {
  token: string
  role: string
  expiresAt: number // unix ms
  email: string // kept for SudoModal re-login + reviewed_by
}

export interface AuthContextValue {
  auth: AuthState | null
  login: (email: string, password: string, role?: string) => Promise<void>
  logout: () => void
}

export const AuthContext = createContext<AuthContextValue | null>(null)

export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext)
  if (!ctx) throw new Error('useAuth must be used within AuthProvider')
  return ctx
}
