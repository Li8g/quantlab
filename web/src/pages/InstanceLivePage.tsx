import { useQuery } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useAuth } from '../auth/AuthContext'
import { apiFetch, ApiError } from '../lib/api'
import { formatAge, formatBtc, formatMs, formatNum, formatUsd } from '../lib/format'
import {
  ConnectionBadge,
  InstanceStatusBadge,
} from '../components/StatusBadge'
import type {
  InstanceLiveResponse,
  PortfolioSnapshotView,
  TradeRecordSummary,
} from '../lib/types'

// F2.2: per-instance live snapshot. Polled every 3s — the detail view is
// the one place a tighter cadence pays off; the underlying tick is 1m so
// this is already faster than the data's own freshness.
export default function InstanceLivePage() {
  const { instanceId = '' } = useParams()
  const { auth } = useAuth()

  const { data, isLoading, error } = useQuery({
    queryKey: ['instance-live', instanceId],
    queryFn: () =>
      apiFetch<InstanceLiveResponse>(`/instances/${instanceId}/live`, {
        token: auth?.token,
      }),
    refetchInterval: 3_000,
  })

  if (isLoading) return <p className="text-sm text-slate-500">Loading…</p>
  if (error) {
    const status = error instanceof ApiError ? error.status : undefined
    return (
      <div className="text-sm">
        <BackLink />
        <p className="mt-4 text-red-600">
          {status === 404 ? 'Instance not found.' : (error as Error).message}
        </p>
      </div>
    )
  }
  if (!data) return null

  const { instance, portfolio, connection, recent_trades } = data

  return (
    <div>
      <BackLink />
      <div className="mt-3 mb-5 flex flex-wrap items-center gap-3">
        <h1 className="text-lg font-semibold text-slate-800">
          {instance.strategy_id}{' '}
          <span className="text-slate-400">/ {instance.pair}</span>
        </h1>
        <InstanceStatusBadge status={instance.status} />
        {connection ? (
          <ConnectionBadge connected={connection.connected} />
        ) : (
          <span className="text-xs text-slate-400">connection: n/a</span>
        )}
        <span className="font-mono text-xs text-slate-400">
          {instance.instance_id}
        </span>
      </div>

      <div className="grid gap-4 md:grid-cols-2">
        <EquityCard portfolio={portfolio} />
        <HoldingsCard portfolio={portfolio} />
      </div>

      <TradesCard trades={recent_trades} />
    </div>
  )
}

function BackLink() {
  return (
    <Link to="/instances" className="text-sm text-slate-500 hover:text-slate-800">
      ← Instances
    </Link>
  )
}

function Card({ title, children }: { title: string; children: ReactNode }) {
  return (
    <div className="rounded-lg border border-slate-200 bg-white p-4">
      <h2 className="mb-3 text-sm font-semibold text-slate-700">{title}</h2>
      {children}
    </div>
  )
}

function EquityCard({ portfolio }: { portfolio?: PortfolioSnapshotView }) {
  return (
    <Card title="Equity">
      {portfolio?.equity != null ? (
        <>
          <div className="text-2xl font-semibold tabular-nums text-slate-900">
            {formatUsd(portfolio.equity)}{' '}
            <span className="text-sm font-normal text-slate-400">USDT</span>
          </div>
          <p className="mt-1 text-xs text-slate-500">
            marked @ {formatUsd(portfolio.mark_price)} · {formatAge(portfolio.mark_price_ms)}
          </p>
        </>
      ) : (
        <p className="text-sm text-slate-400">
          {portfolio
            ? 'No mark price — equity unavailable.'
            : 'No portfolio yet (instance has not ticked).'}
        </p>
      )}
    </Card>
  )
}

function HoldingsCard({ portfolio }: { portfolio?: PortfolioSnapshotView }) {
  if (!portfolio)
    return (
      <Card title="Holdings">
        <p className="text-sm text-slate-400">—</p>
      </Card>
    )
  const rows: [string, string][] = [
    ['Float BTC', formatBtc(portfolio.float_btc)],
    ['Dead BTC', formatBtc(portfolio.dead_btc)],
    ['Cold-sealed BTC', formatBtc(portfolio.cold_sealed_btc)],
    ['USDT', formatUsd(portfolio.usdt)],
  ]
  return (
    <Card title="Holdings">
      <dl className="space-y-1 text-sm">
        {rows.map(([k, v]) => (
          <div key={k} className="flex justify-between">
            <dt className="text-slate-500">{k}</dt>
            <dd className="tabular-nums text-slate-900">{v}</dd>
          </div>
        ))}
      </dl>
      <p className="mt-2 text-xs text-slate-400">
        as of {formatMs(portfolio.now_ms)}
      </p>
    </Card>
  )
}

function TradesCard({ trades }: { trades: TradeRecordSummary[] }) {
  return (
    <div className="mt-4">
      <h2 className="mb-2 text-sm font-semibold text-slate-700">
        Recent trades
      </h2>
      {trades.length === 0 ? (
        <p className="text-sm text-slate-400">No trades yet.</p>
      ) : (
        <div className="overflow-hidden rounded-lg border border-slate-200 bg-white">
          <table className="w-full text-sm">
            <thead className="bg-slate-50 text-left text-slate-500">
              <tr>
                <th className="px-4 py-2 font-medium">Side</th>
                <th className="px-4 py-2 font-medium">Type</th>
                <th className="px-4 py-2 font-medium">Qty USD</th>
                <th className="px-4 py-2 font-medium">Status</th>
                <th className="px-4 py-2 font-medium">Fills</th>
                <th className="px-4 py-2 font-medium">Created</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {trades.map((t) => (
                <TradeRow key={t.client_order_id} t={t} />
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

function TradeRow({ t }: { t: TradeRecordSummary }) {
  const fills = t.fills ?? []
  return (
    <>
      <tr>
        <td className="px-4 py-2 capitalize">{t.side}</td>
        <td className="px-4 py-2">{t.order_type}</td>
        <td className="px-4 py-2 tabular-nums">{formatUsd(t.quantity_usd)}</td>
        <td className="px-4 py-2">{t.status}</td>
        <td className="px-4 py-2 text-slate-500">{fills.length || '—'}</td>
        <td className="px-4 py-2">{formatMs(t.created_at_ms)}</td>
      </tr>
      {fills.map((f) => (
        <tr key={f.exchange_order_id} className="bg-slate-50/60 text-xs text-slate-500">
          <td className="px-4 py-1" />
          <td className="px-4 py-1" colSpan={2}>
            filled {formatBtc(f.fill_quantity)} @ {formatUsd(f.fill_price)}
          </td>
          <td className="px-4 py-1" colSpan={2}>
            fee {formatNum(f.fill_fee_amount, 6)} {f.fill_fee_asset} · slip{' '}
            {formatNum(f.actual_slippage_bps, 1)} bps
          </td>
          <td className="px-4 py-1">{formatMs(f.filled_at_exchange_ms)}</td>
        </tr>
      ))}
    </>
  )
}
