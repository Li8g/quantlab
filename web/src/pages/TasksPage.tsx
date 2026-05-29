import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { apiFetch } from '../lib/api'
import { formatMs } from '../lib/format'
import { TaskStatusBadge } from '../components/StatusBadge'
import type { ListTasksResponse } from '../lib/types'

// F1.1: evolution tasks list — the entry point for challenger discovery.
// There's no "list challengers" endpoint, so the path to a challenger is
// task → its winning challenger_id (resolved on the task detail page).
export default function TasksPage() {
  const { auth } = useAuth()
  const navigate = useNavigate()

  const { data, isLoading, error } = useQuery({
    queryKey: ['tasks'],
    queryFn: () =>
      apiFetch<ListTasksResponse>('/evolution/tasks?limit=50', {
        token: auth?.token,
      }),
  })

  if (isLoading) return <p className="text-sm text-slate-500">Loading…</p>
  if (error)
    return <p className="text-sm text-red-600">{(error as Error).message}</p>

  const items = data?.items ?? []
  if (items.length === 0)
    return <p className="text-sm text-slate-500">No evolution tasks yet.</p>

  return (
    <div>
      <h1 className="mb-4 text-lg font-semibold text-slate-800">
        Evolution Tasks
      </h1>
      <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-left text-slate-500">
            <tr>
              <th className="px-4 py-2 font-medium">Task</th>
              <th className="px-4 py-2 font-medium">Strategy</th>
              <th className="px-4 py-2 font-medium">Pair</th>
              <th className="px-4 py-2 font-medium">Interval</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium">Created</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {items.map((t) => (
              <tr
                key={t.task_id}
                onClick={() => navigate(`/tasks/${t.task_id}`)}
                className="cursor-pointer hover:bg-slate-50"
              >
                <td className="px-4 py-2 font-mono text-xs">
                  {t.task_id.slice(0, 12)}
                </td>
                <td className="px-4 py-2">{t.strategy_id}</td>
                <td className="px-4 py-2">{t.pair}</td>
                <td className="px-4 py-2">{t.interval}</td>
                <td className="px-4 py-2">
                  <TaskStatusBadge status={t.status} />
                </td>
                <td className="px-4 py-2">{formatMs(t.created_at_ms)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
