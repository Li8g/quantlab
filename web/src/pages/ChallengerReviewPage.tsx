import { useState, type ReactNode } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useParams } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { SudoModal } from '../auth/SudoModal'
import { apiFetch } from '../lib/api'
import { formatMs } from '../lib/format'
import { DecisionStatusBadge, OosBadge } from '../components/StatusBadge'
import type {
  ChallengerPackage,
  ChallengerSummary,
  EvolutionTaskStatusResponse,
  WindowScore,
} from '../lib/types'

function fmtNum(n: number | undefined, digits = 4): string {
  return n == null ? '—' : n.toFixed(digits)
}

function Stat({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div>
      <div className="text-xs text-slate-500">{label}</div>
      <div className="text-sm font-medium text-slate-800">{value}</div>
    </div>
  )
}

function Panel({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-5">
      <h2 className="mb-3 text-sm font-semibold text-slate-700">{title}</h2>
      {children}
    </div>
  )
}

// One window's score is three-state: normal value, self-fatal, or
// cascade-skipped. Never dereference a null value as a number.
function windowCell(w: WindowScore) {
  if (w.skipped_by)
    return <span className="text-slate-400">skipped ({w.skipped_by})</span>
  if (w.score.fatal)
    return (
      <span className="text-red-600">
        fatal{w.fatal_reason ? ` (${w.fatal_reason})` : ''}
      </span>
    )
  return <span className="font-mono">{fmtNum(w.score.value)}</span>
}

// F1.2 + F1.3: resolve task → winning challenger, then render the full
// review (summary + package). Reached from the Tasks list.
export default function ChallengerReviewPage() {
  const { taskId } = useParams<{ taskId: string }>()
  const { auth } = useAuth()
  const token = auth?.token
  const queryClient = useQueryClient()
  const [sudoOpen, setSudoOpen] = useState(false)

  const taskQ = useQuery({
    queryKey: ['task', taskId],
    queryFn: () =>
      apiFetch<EvolutionTaskStatusResponse>(`/evolution/tasks/${taskId}`, {
        token,
      }),
    enabled: !!taskId,
  })

  const challengerId = taskQ.data?.challenger_id

  const summaryQ = useQuery({
    queryKey: ['challenger', challengerId],
    queryFn: () =>
      apiFetch<ChallengerSummary>(`/challengers/${challengerId}`, { token }),
    enabled: !!challengerId,
  })

  const packageQ = useQuery({
    queryKey: ['challenger-package', challengerId],
    queryFn: () =>
      apiFetch<ChallengerPackage>(`/challengers/${challengerId}/package`, {
        token,
      }),
    enabled: !!challengerId,
  })

  if (taskQ.isLoading)
    return <p className="text-sm text-slate-500">Loading task…</p>
  if (taskQ.error)
    return <p className="text-sm text-red-600">{(taskQ.error as Error).message}</p>

  // Task hasn't produced a challenger (still running, or failed).
  if (!challengerId)
    return (
      <Panel title="No challenger yet">
        <p className="text-sm text-slate-500">
          Task status: <span className="font-medium">{taskQ.data?.status}</span>
          {taskQ.data?.failure_reason
            ? ` — ${taskQ.data.failure_reason}`
            : ''}
        </p>
      </Panel>
    )

  const s = summaryQ.data
  const pkg = packageQ.data
  const canPromote = s?.decision_status === 'pending' && !s.test_mode

  return (
    <div className="space-y-5">
      <div className="flex items-center gap-3">
        <h1 className="text-lg font-semibold text-slate-800">Challenger</h1>
        <span className="font-mono text-xs text-slate-500">{challengerId}</span>
        {s && <DecisionStatusBadge status={s.decision_status} />}
        {s?.test_mode && (
          <span className="rounded bg-amber-100 px-2 py-0.5 text-xs font-medium text-amber-700">
            test_mode — not promotable
          </span>
        )}
        {canPromote && (
          <button
            type="button"
            onClick={() => setSudoOpen(true)}
            className="ml-auto rounded-md bg-green-700 px-3 py-1.5 text-sm font-medium text-white hover:bg-green-800"
          >
            Promote
          </button>
        )}
      </div>

      {sudoOpen && auth && challengerId && (
        <SudoModal
          action={`Promote challenger ${challengerId.slice(0, 12)} to champion.`}
          email={auth.email}
          onClose={() => setSudoOpen(false)}
          onConfirm={async (adminToken, note) => {
            await apiFetch(`/challengers/${challengerId}/promote`, {
              method: 'POST',
              token: adminToken,
              body: {
                reviewed_by: auth.email,
                decision_note: note || undefined,
              },
            })
            await queryClient.invalidateQueries({
              queryKey: ['challenger', challengerId],
            })
          }}
        />
      )}

      {summaryQ.isLoading && (
        <p className="text-sm text-slate-500">Loading summary…</p>
      )}
      {s && (
        <Panel title="Scores">
          <div className="grid grid-cols-2 gap-4 sm:grid-cols-4">
            <Stat label="Strategy / Pair" value={`${s.strategy_id} · ${s.pair}`} />
            <Stat label="ScoreTotal" value={fmtNum(s.score_total)} />
            <Stat label="ScoreRaw" value={fmtNum(s.score_raw)} />
            <Stat label="Consistency penalty" value={fmtNum(s.consistency_penalty)} />
            <Stat label="DSR" value={fmtNum(s.dsr)} />
            <Stat label="Promoted" value={formatMs(s.promoted_at_ms)} />
            <Stat label="Retired" value={formatMs(s.retired_at_ms)} />
          </div>
        </Panel>
      )}

      {packageQ.isLoading && (
        <p className="text-sm text-slate-500">Loading package…</p>
      )}
      {pkg && (
        <>
          <Panel title="Crucible windows (6m → 2y → 5y → 10y)">
            <table className="w-full text-sm">
              <thead className="text-left text-slate-500">
                <tr>
                  <th className="py-1 font-medium">Window</th>
                  <th className="py-1 font-medium">Score</th>
                  <th className="py-1 font-medium">Bars evaluated</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-slate-100">
                {pkg.evaluation.window_scores.map((w) => (
                  <tr key={w.window}>
                    <td className="py-1.5 font-medium">{w.window}</td>
                    <td className="py-1.5">{windowCell(w)}</td>
                    <td className="py-1.5 text-slate-500">
                      {w.bars_evaluated.toLocaleString()}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          </Panel>

          <div className="grid gap-5 sm:grid-cols-2">
            <Panel title="OOS verification">
              <div className="mb-2">
                <OosBadge
                  status={pkg.verification.oos_result.status}
                  color={pkg.verification.oos_result.decision_color}
                />
              </div>
              <div className="grid grid-cols-2 gap-3">
                <Stat
                  label="Alpha monthly"
                  value={fmtNum(pkg.verification.oos_result.oos_alpha_monthly)}
                />
                <Stat
                  label="Alpha weekly"
                  value={fmtNum(pkg.verification.oos_result.oos_alpha_weekly)}
                />
              </div>
              {pkg.verification.oos_result.notes && (
                <p className="mt-3 text-xs text-slate-500">
                  {pkg.verification.oos_result.notes}
                </p>
              )}
            </Panel>

            <Panel title="Friction (effective)">
              <div className="grid grid-cols-2 gap-3">
                <Stat
                  label="Taker fee (bps)"
                  value={pkg.evaluation.friction_actual.taker_fee_bps}
                />
                <Stat
                  label="Slippage (bps)"
                  value={pkg.evaluation.friction_actual.slippage_bps}
                />
              </div>
            </Panel>
          </div>
        </>
      )}
    </div>
  )
}
