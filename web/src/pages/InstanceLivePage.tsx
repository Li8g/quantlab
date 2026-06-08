import { useState } from 'react'
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useParams } from 'react-router-dom'
import type { ReactNode } from 'react'
import { useAuth } from '../auth/context'
import { SudoModal } from '../auth/SudoModal'
import { apiFetch, ApiError } from '../lib/api'
import { formatAge, formatBtc, formatMs, formatNum, formatUsd } from '../lib/format'
import {
  ConnectionBadge,
  InstanceStatusBadge,
} from '../components/StatusBadge'
import type {
  AgentErrorView,
  InstanceLiveResponse,
  InstanceStatus,
  KillStatusView,
  PortfolioSnapshotView,
  ReconciliationDiscrepancyView,
  TradeRecordSummary,
} from '../lib/types'

// The intervention verbs. start/stop/deploy (場景② F2.3) require operator+;
// resume (§5.13 v2 — lift the kill_switch freeze) is admin-only. Each
// routes through a SudoModal step-up before hitting the endpoint; the role
// it requests is derived from the verb (see sudoRoleForVerb).
type ActionVerb = 'start' | 'stop' | 'deploy' | 'resume'

// resume un-freezes a halted agent (admin); the rest are operator-level.
function sudoRoleForVerb(verb: ActionVerb): 'admin' | 'operator' {
  return verb === 'resume' ? 'admin' : 'operator'
}

// F2.2: per-instance live snapshot. Polled every 3s — the detail view is
// the one place a tighter cadence pays off; the underlying tick is 1m so
// this is already faster than the data's own freshness.
export default function InstanceLivePage() {
  const { instanceId = '' } = useParams()
  const { auth } = useAuth()
  const queryClient = useQueryClient()

  // Pending intervention (null = no modal). desc is the human-readable
  // action line shown in the step-up modal.
  const [pending, setPending] = useState<{ verb: ActionVerb; desc: string } | null>(null)
  const [deployId, setDeployId] = useState('')

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
  const discrepancies = data.recent_discrepancies ?? []
  const agentErrors = data.recent_errors ?? []

  function requestAction(verb: ActionVerb) {
    const tag = `${instance.strategy_id} · ${instance.pair}`
    const desc =
      verb === 'start'
        ? `Start live trading on ${tag}.`
        : verb === 'stop'
          ? `Pause ${tag}.`
          : verb === 'resume'
            ? `Resume (un-freeze) the agent on ${tag}.`
            : `Deploy champion ${deployId.trim()} to ${tag}.`
    setPending({ verb, desc })
  }

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

      {data.kill_status && (
        <KillBanner kill={data.kill_status} onResume={() => requestAction('resume')} />
      )}

      <ControlsCard
        status={instance.status}
        activeChampionId={instance.active_champion_id}
        deployId={deployId}
        setDeployId={setDeployId}
        onAct={requestAction}
      />

      <div className="mt-4 grid gap-4 md:grid-cols-2">
        <EquityCard portfolio={portfolio} maxBarStalenessMs={data.max_bar_staleness_ms} />
        <HoldingsCard portfolio={portfolio} />
      </div>

      <TradesCard trades={recent_trades} />

      <ReconciliationCard rows={discrepancies} />
      <AgentErrorsCard rows={agentErrors} />

      {pending && auth && (
        <SudoModal
          role={sudoRoleForVerb(pending.verb)}
          action={pending.desc}
          email={auth.email}
          onClose={() => setPending(null)}
          onConfirm={async (token) => {
            const base = `/instances/${instanceId}`
            if (pending.verb === 'start') {
              await apiFetch(`${base}/start`, { method: 'POST', token })
            } else if (pending.verb === 'stop') {
              await apiFetch(`${base}/stop`, { method: 'POST', token })
            } else if (pending.verb === 'resume') {
              await apiFetch(`${base}/resume`, { method: 'POST', token })
            } else {
              await apiFetch(`${base}/deploy-champion`, {
                method: 'POST',
                token,
                body: { challenger_id: deployId.trim() },
              })
              setDeployId('')
            }
            await queryClient.invalidateQueries({
              queryKey: ['instance-live', instanceId],
            })
            await queryClient.invalidateQueries({ queryKey: ['instances'] })
          }}
        />
      )}
    </div>
  )
}

// Controls (F2.3): Start (go live) / Pause / Deploy champion. Buttons are
// status-aware — a retired instance refuses every transition (backend
// 422), and start/pause toggle on the live state. Visible to all; the
// operator step-up at click time is the real gate.
function ControlsCard({
  status,
  activeChampionId,
  deployId,
  setDeployId,
  onAct,
}: {
  status: InstanceStatus
  activeChampionId?: string
  deployId: string
  setDeployId: (v: string) => void
  onAct: (verb: ActionVerb) => void
}) {
  const btn =
    'rounded-md border border-slate-300 px-3 py-1.5 text-sm font-medium text-slate-700 hover:bg-slate-100 disabled:opacity-40 disabled:hover:bg-transparent'
  return (
    <Card title="Controls">
      {status === 'retired' ? (
        <p className="text-sm text-slate-400">
          Instance retired — no actions available.
        </p>
      ) : (
        <div className="flex flex-wrap items-center gap-3">
          <button
            type="button"
            className={btn}
            disabled={status === 'live'}
            onClick={() => onAct('start')}
          >
            Start
          </button>
          <button
            type="button"
            className={btn}
            disabled={status !== 'live'}
            onClick={() => onAct('stop')}
          >
            Pause
          </button>
          <div className="ml-auto flex items-center gap-2">
            <input
              value={deployId}
              onChange={(e) => setDeployId(e.target.value)}
              placeholder="challenger_id"
              className="w-64 rounded-md border border-slate-300 px-2 py-1.5 font-mono text-xs focus:border-slate-500 focus:outline-none"
            />
            <button
              type="button"
              className={btn}
              disabled={!deployId.trim()}
              onClick={() => onAct('deploy')}
            >
              Deploy champion
            </button>
          </div>
        </div>
      )}
      {activeChampionId && (
        <p className="mt-3 text-xs text-slate-400">
          active champion:{' '}
          <span className="font-mono text-slate-500">{activeChampionId}</span>
        </p>
      )}
    </Card>
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

// DEFAULT_STALENESS_MS is the fallback when the server does not send
// max_bar_staleness_ms (old server / cold-start). Matches DefaultMaxBarStaleness.
const DEFAULT_STALENESS_MS = 15 * 60 * 1000

function EquityCard({
  portfolio,
  maxBarStalenessMs,
}: {
  portfolio?: PortfolioSnapshotView
  maxBarStalenessMs?: number
}) {
  const threshold = maxBarStalenessMs ?? DEFAULT_STALENESS_MS

  // Staleness colour: yellow when age > 50% of threshold, red when past threshold.
  function ageClass(markPriceMs: number): string {
    const ageMs = Date.now() - markPriceMs
    if (ageMs >= threshold) return 'text-red-600 font-medium'
    if (ageMs >= threshold * 0.5) return 'text-amber-600'
    return 'text-slate-500'
  }

  function staleLabel(markPriceMs: number): string | null {
    const ageMs = Date.now() - markPriceMs
    if (ageMs >= threshold) return ' — data stale, trading halted'
    return null
  }

  return (
    <Card title="Equity">
      {portfolio?.equity != null ? (
        <>
          <div className="text-2xl font-semibold tabular-nums text-slate-900">
            {formatUsd(portfolio.equity)}{' '}
            <span className="text-sm font-normal text-slate-400">USDT</span>
          </div>
          <p className={`mt-1 text-xs ${ageClass(portfolio.mark_price_ms)}`}>
            marked @ {formatUsd(portfolio.mark_price)} · {formatAge(portfolio.mark_price_ms)}
            {staleLabel(portfolio.mark_price_ms)}
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

// amountByAsset formats a holdings amount with the right precision for the
// asset (USDT → 2dp money, base asset → 8dp).
function amountByAsset(asset: string, v: number) {
  return asset === 'USDT' ? formatUsd(v) : formatBtc(v)
}

// Frozen banner (Option 3 step 4): shown only while the account is
// currently frozen — the backend surfaces kill_status from the latest
// kill/resume event (LatestKillOrResume), so a resume clears this banner.
// The Resume button issues the §5.13 v2 un-freeze (admin step-up); it
// lifts the latch without a process restart.
function KillBanner({ kill, onResume }: { kill: KillStatusView; onResume: () => void }) {
  const when = new Date(kill.killed_at_ms).toLocaleString()
  const how = kill.trigger === 'auto' ? 'Auto-frozen' : 'Manually killed'
  return (
    <div className="mb-4 rounded-lg border border-red-300 bg-red-50 p-4 text-sm text-red-800">
      <div className="flex flex-wrap items-center gap-3">
        <div className="font-semibold">⚠️ Agent frozen — kill_switch active</div>
        <button
          type="button"
          className="ml-auto rounded-md border border-red-400 bg-white px-3 py-1.5 text-sm font-medium text-red-700 hover:bg-red-100"
          onClick={onResume}
        >
          Resume agent
        </button>
      </div>
      <div className="mt-1">
        {how} · reason <span className="font-mono">{kill.reason}</span> · by{' '}
        <span className="font-mono">{kill.actor}</span> · {when}
      </div>
      <div className="mt-1 text-xs text-red-600">
        New orders are rejected until the agent is resumed (admin).
      </div>
    </div>
  )
}

// Reconciliation panel (Tier L): position drift the Agent reported vs
// SaaS bookkeeping (Phase 8 持仓对账). Empty = books match the exchange.
function ReconciliationCard({ rows }: { rows: ReconciliationDiscrepancyView[] }) {
  return (
    <div className="mt-4">
      <h2 className="mb-2 text-sm font-semibold text-slate-700">
        Reconciliation
      </h2>
      {rows.length === 0 ? (
        <p className="text-sm text-emerald-600">
          ✓ No discrepancies — books match the exchange.
        </p>
      ) : (
        <div className="overflow-hidden rounded-lg border border-red-200 bg-white">
          <table className="w-full text-sm">
            <thead className="bg-red-50 text-left text-red-700">
              <tr>
                <th className="px-4 py-2 font-medium">Asset</th>
                <th className="px-4 py-2 font-medium">Expected</th>
                <th className="px-4 py-2 font-medium">Actual</th>
                <th className="px-4 py-2 font-medium">Diff</th>
                <th className="px-4 py-2 font-medium">Drift</th>
                <th className="px-4 py-2 font-medium">Detected</th>
              </tr>
            </thead>
            <tbody className="divide-y divide-slate-100">
              {rows.map((d) => (
                <tr key={`${d.asset}-${d.detected_at_ms}`}>
                  <td className="px-4 py-2 font-medium">{d.asset}</td>
                  <td className="px-4 py-2 tabular-nums">
                    {amountByAsset(d.asset, d.expected_amount)}
                  </td>
                  <td className="px-4 py-2 tabular-nums">
                    {amountByAsset(d.asset, d.actual_amount)}
                  </td>
                  <td className="px-4 py-2 tabular-nums text-red-600">
                    {amountByAsset(d.asset, d.diff_amount)}
                  </td>
                  <td className="px-4 py-2 tabular-nums">
                    {formatNum(d.drift_bps, 1)} bps
                  </td>
                  <td className="px-4 py-2">{formatMs(d.detected_at_ms)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  )
}

// Agent error stream (Tier L): exchange-layer errors the Agent collected
// (rate limits, partial outages) reported via delta_report.
function AgentErrorsCard({ rows }: { rows: AgentErrorView[] }) {
  return (
    <div className="mt-4">
      <h2 className="mb-2 text-sm font-semibold text-slate-700">
        Agent errors
      </h2>
      {rows.length === 0 ? (
        <p className="text-sm text-slate-400">No agent errors.</p>
      ) : (
        <ul className="space-y-1">
          {rows.map((e) => (
            <li
              key={`${e.code}-${e.occurred_at_ms}`}
              className="rounded-md border border-amber-200 bg-amber-50 px-3 py-2 text-sm"
            >
              <span className="font-mono text-xs font-medium text-amber-800">
                {e.code}
              </span>
              <span className="ml-2 text-slate-600">{e.message}</span>
              <span className="ml-2 text-xs text-slate-400">
                {formatMs(e.occurred_at_ms)}
              </span>
            </li>
          ))}
        </ul>
      )}
    </div>
  )
}
