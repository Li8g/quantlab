import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useAuth } from '../auth/AuthContext'
import { SudoModal } from '../auth/SudoModal'
import { apiFetch } from '../lib/api'
import { formatMs } from '../lib/format'
import type {
  ChampionHistoryEntry,
  ListChampionHistoryResponse,
} from '../lib/types'

// F0.4: the first real read view — champion promotion history. This is
// also the F1 starting surface (drill into a challenger from here).
export default function ChampionsPage() {
  const { auth } = useAuth()
  const queryClient = useQueryClient()
  // The active champion targeted for retirement (null = modal closed).
  const [retiring, setRetiring] = useState<ChampionHistoryEntry | null>(null)

  const { data, isLoading, error } = useQuery({
    queryKey: ['champions-history'],
    queryFn: () =>
      apiFetch<ListChampionHistoryResponse>('/champions/history?limit=50', {
        token: auth?.token,
      }),
  })

  if (isLoading) return <p className="text-sm text-slate-500">Loading…</p>
  if (error)
    return <p className="text-sm text-red-600">{(error as Error).message}</p>

  const items = data?.items ?? []
  if (items.length === 0)
    return <p className="text-sm text-slate-500">No champion history yet.</p>

  return (
    <div>
      <h1 className="mb-4 text-lg font-semibold text-slate-800">
        Champion History
      </h1>
      <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-left text-slate-500">
            <tr>
              <th className="px-4 py-2 font-medium">Strategy</th>
              <th className="px-4 py-2 font-medium">Pair</th>
              <th className="px-4 py-2 font-medium">Challenger</th>
              <th className="px-4 py-2 font-medium">Promoted</th>
              <th className="px-4 py-2 font-medium">Retired</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium"></th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {items.map((c) => (
              <tr key={c.id} className="hover:bg-slate-50">
                <td className="px-4 py-2">{c.strategy_id}</td>
                <td className="px-4 py-2">{c.pair}</td>
                <td className="px-4 py-2 font-mono text-xs">
                  {c.challenger_id}
                </td>
                <td className="px-4 py-2">{formatMs(c.promoted_at_ms)}</td>
                <td className="px-4 py-2">{formatMs(c.retired_at_ms)}</td>
                <td className="px-4 py-2">
                  {c.retired_at_ms ? (
                    <span className="rounded bg-slate-100 px-2 py-0.5 text-xs text-slate-600">
                      retired
                    </span>
                  ) : (
                    <span className="rounded bg-green-100 px-2 py-0.5 text-xs text-green-700">
                      active
                    </span>
                  )}
                </td>
                <td className="px-4 py-2 text-right">
                  {!c.retired_at_ms && (
                    <button
                      type="button"
                      onClick={() => setRetiring(c)}
                      className="rounded-md border border-slate-300 px-2 py-1 text-xs font-medium text-slate-700 hover:bg-slate-100"
                    >
                      Retire
                    </button>
                  )}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>

      {retiring && auth && (
        <SudoModal
          action={`Retire the active ${retiring.strategy_id} · ${retiring.pair} champion.`}
          email={auth.email}
          onClose={() => setRetiring(null)}
          onConfirm={async (adminToken, note) => {
            await apiFetch(`/champions/${retiring.challenger_id}/retire`, {
              method: 'POST',
              token: adminToken,
              body: {
                reviewed_by: auth.email,
                decision_note: note || undefined,
              },
            })
            await queryClient.invalidateQueries({
              queryKey: ['champions-history'],
            })
          }}
        />
      )}
    </div>
  )
}
