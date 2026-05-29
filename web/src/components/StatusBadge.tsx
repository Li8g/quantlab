import type {
  DecisionColor,
  DecisionStatus,
  InstanceStatus,
  TaskStatus,
  VerificationStatus,
} from '../lib/types'

// Task status → tailwind color. Mirrors resultpkg.TaskStatus.
const TASK_COLORS: Record<TaskStatus, string> = {
  queued: 'bg-slate-100 text-slate-600',
  running: 'bg-blue-100 text-blue-700',
  succeeded: 'bg-green-100 text-green-700',
  failed: 'bg-red-100 text-red-700',
  cancelled: 'bg-amber-100 text-amber-700',
}

function Badge({ color, label }: { color: string; label: string }) {
  return (
    <span className={`rounded px-2 py-0.5 text-xs font-medium ${color}`}>
      {label}
    </span>
  )
}

export function TaskStatusBadge({ status }: { status: TaskStatus }) {
  return <Badge color={TASK_COLORS[status] ?? 'bg-slate-100 text-slate-600'} label={status} />
}

const DECISION_COLORS: Record<DecisionStatus, string> = {
  promoted: 'bg-green-100 text-green-700',
  rejected: 'bg-red-100 text-red-700',
  pending: 'bg-slate-100 text-slate-600',
}

export function DecisionStatusBadge({ status }: { status: DecisionStatus }) {
  return <Badge color={DECISION_COLORS[status] ?? 'bg-slate-100 text-slate-600'} label={status} />
}

// OOS verification: show the asymmetric decision color when present
// (status=ok), else the raw status (not_run / insufficient_data / failed).
const OOS_COLOR: Record<DecisionColor, string> = {
  green: 'bg-green-100 text-green-700',
  yellow: 'bg-amber-100 text-amber-700',
  red: 'bg-red-100 text-red-700',
  gray: 'bg-slate-100 text-slate-600',
}
const OOS_STATUS_COLOR: Record<VerificationStatus, string> = {
  ok: 'bg-green-100 text-green-700',
  insufficient_data: 'bg-amber-100 text-amber-700',
  failed: 'bg-red-100 text-red-700',
  not_run: 'bg-slate-100 text-slate-600',
}

export function OosBadge({
  status,
  color,
}: {
  status: VerificationStatus
  color?: DecisionColor
}) {
  if (color) return <Badge color={OOS_COLOR[color]} label={`OOS ${color}`} />
  return <Badge color={OOS_STATUS_COLOR[status] ?? 'bg-slate-100 text-slate-600'} label={status} />
}

// Instance lifecycle status → color. Mirrors store.InstanceStatus.
const INSTANCE_COLORS: Record<InstanceStatus, string> = {
  live: 'bg-green-100 text-green-700',
  paused: 'bg-amber-100 text-amber-700',
  idle: 'bg-slate-100 text-slate-600',
  retired: 'bg-slate-200 text-slate-500',
}

export function InstanceStatusBadge({ status }: { status: InstanceStatus }) {
  return (
    <Badge
      color={INSTANCE_COLORS[status] ?? 'bg-slate-100 text-slate-600'}
      label={status}
    />
  )
}

// Agent connection presence (live monitor). Green dot = connected.
export function ConnectionBadge({ connected }: { connected: boolean }) {
  return (
    <Badge
      color={connected ? 'bg-green-100 text-green-700' : 'bg-red-100 text-red-700'}
      label={connected ? '● connected' : '● offline'}
    />
  )
}
