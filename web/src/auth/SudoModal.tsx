import { useState, type FormEvent } from 'react'
import { apiFetch, ApiError } from '../lib/api'
import type { LoginResponse } from '../lib/types'

// Sudo-style step-up (docs/frontend-promote-retire-v1.md §3): privileged
// actions don't ride the standing viewer session. Rather than hold a
// long-lived elevated token, we re-authenticate for a fresh token at the
// requested role right before the action, pass it to onConfirm, and let
// it fall out of scope immediately after. role defaults to admin
// (promote/retire); live-monitor interventions pass "operator".
export function SudoModal({
  action,
  email,
  role = 'admin',
  onConfirm,
  onClose,
}: {
  action: string
  email: string
  role?: 'operator' | 'admin'
  onConfirm: (token: string, note: string) => Promise<void>
  onClose: () => void
}) {
  const [password, setPassword] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)

  async function onSubmit(e: FormEvent) {
    e.preventDefault()
    setError(null)
    setBusy(true)
    try {
      // Step up to a fresh token at the requested role, then run the
      // action with it.
      const res = await apiFetch<LoginResponse>('/auth/login', {
        method: 'POST',
        body: { email, password, role },
        skipUnauthorizedHandler: true,
      })
      await onConfirm(res.token, note.trim())
      onClose()
    } catch (err) {
      if (err instanceof ApiError && err.status === 400)
        setError(`Your account lacks ${role} permission.`)
      else if (err instanceof ApiError && err.status === 401)
        setError('Wrong password.')
      else setError(err instanceof Error ? err.message : 'action failed')
    } finally {
      setBusy(false)
    }
  }

  return (
    <div className="fixed inset-0 z-50 flex items-center justify-center bg-black/40">
      <form
        onSubmit={onSubmit}
        className="w-full max-w-sm space-y-4 rounded-lg bg-white p-6 shadow-xl"
      >
        <div>
          <h2 className="text-base font-semibold text-slate-800">
            {role === 'admin' ? 'Admin' : 'Operator'} confirmation
          </h2>
          <p className="mt-1 text-sm text-slate-500">{action}</p>
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700">
            Password for {email}
          </label>
          <input
            type="password"
            autoComplete="current-password"
            autoFocus
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            className="w-full rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
            required
          />
        </div>
        <div>
          <label className="mb-1 block text-sm font-medium text-slate-700">
            Note <span className="text-slate-400">(optional)</span>
          </label>
          <textarea
            value={note}
            onChange={(e) => setNote(e.target.value)}
            rows={2}
            className="w-full rounded-md border border-slate-300 px-3 py-2 text-sm focus:border-slate-500 focus:outline-none"
          />
        </div>
        {error && <p className="text-sm text-red-600">{error}</p>}
        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            disabled={busy}
            className="rounded-md px-3 py-2 text-sm font-medium text-slate-600 hover:bg-slate-100 disabled:opacity-50"
          >
            Cancel
          </button>
          <button
            type="submit"
            disabled={busy}
            className="rounded-md bg-slate-900 px-3 py-2 text-sm font-medium text-white hover:bg-slate-700 disabled:opacity-50"
          >
            {busy ? 'Confirming…' : 'Confirm'}
          </button>
        </div>
      </form>
    </div>
  )
}
