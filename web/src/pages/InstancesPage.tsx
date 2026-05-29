import { useQuery } from '@tanstack/react-query'
import { useNavigate } from 'react-router-dom'
import { useAuth } from '../auth/AuthContext'
import { apiFetch } from '../lib/api'
import { formatMs } from '../lib/format'
import { InstanceStatusBadge } from '../components/StatusBadge'
import type { InstanceListResponse } from '../lib/types'

// F2.1: live instance list. Owner-scoped server-side (admins see all).
// Polled every 15s — the list only changes on cron tick (1m) or
// promote/retire, so a tighter cadence buys nothing.
export default function InstancesPage() {
  const { auth } = useAuth()
  const navigate = useNavigate()

  const { data, isLoading, error } = useQuery({
    queryKey: ['instances'],
    queryFn: () =>
      apiFetch<InstanceListResponse>('/instances', { token: auth?.token }),
    refetchInterval: 15_000,
  })

  if (isLoading) return <p className="text-sm text-slate-500">Loading…</p>
  if (error)
    return <p className="text-sm text-red-600">{(error as Error).message}</p>

  const items = data?.items ?? []
  if (items.length === 0)
    return <p className="text-sm text-slate-500">No live instances.</p>

  return (
    <div>
      <h1 className="mb-4 text-lg font-semibold text-slate-800">
        Live Instances
      </h1>
      <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
        <table className="w-full text-sm">
          <thead className="bg-slate-50 text-left text-slate-500">
            <tr>
              <th className="px-4 py-2 font-medium">Instance</th>
              <th className="px-4 py-2 font-medium">Strategy</th>
              <th className="px-4 py-2 font-medium">Pair</th>
              <th className="px-4 py-2 font-medium">Status</th>
              <th className="px-4 py-2 font-medium">Last tick</th>
            </tr>
          </thead>
          <tbody className="divide-y divide-slate-100">
            {items.map((inst) => (
              <tr
                key={inst.instance_id}
                onClick={() => navigate(`/instances/${inst.instance_id}`)}
                className="cursor-pointer hover:bg-slate-50"
              >
                <td className="px-4 py-2 font-mono text-xs">
                  {inst.instance_id.slice(0, 12)}
                </td>
                <td className="px-4 py-2">{inst.strategy_id}</td>
                <td className="px-4 py-2">{inst.pair}</td>
                <td className="px-4 py-2">
                  <InstanceStatusBadge status={inst.status} />
                </td>
                <td className="px-4 py-2">
                  {formatMs(inst.last_tick_wall_time_ms)}
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
