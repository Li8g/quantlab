import { useState, type FormEvent } from 'react'
import { apiFetch, ApiError } from '../lib/api'
import type { LoginResponse } from '../lib/types'

// Sudo-style step-up (docs/frontend-promote-retire-v1.md §3): promote and
// retire are admin-only. Rather than hold a long-lived admin token, we
// re-authenticate for a short-TTL admin token right before the action,
// pass it to onConfirm, and let it fall out of scope immediately after.
// The standing viewer session in AuthContext is untouched.
export function SudoModal({
  action,
  email,
  onConfirm,
  onClose,
}: {
  action: string
  email: string
  onConfirm: (adminToken: string, note: string) => Promise<void>
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
      // Step up to a short-TTL admin token, then run the action with it.
      const res = await apiFetch<LoginResponse>('/auth/login', {
        method: 'POST',
        body: { email, password, role: 'admin' },
      })
      await onConfirm(res.token, note.trim())
      onClose()
    } catch (err) {
      if (err instanceof ApiError && err.status === 400)
        setError('Your account lacks admin permission.')
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
            Admin confirmation
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
