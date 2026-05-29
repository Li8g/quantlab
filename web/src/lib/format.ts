// Epoch-ms formatting. All API timestamps are unix milliseconds.

export function formatMs(ms: number | undefined | null): string {
  if (ms == null) return '—'
  return new Date(ms).toLocaleString()
}

// Fixed-digit number formatting; em-dash for nullish. USDT uses 2
// digits, BTC quantities 8 (satoshi resolution).
export function formatNum(
  n: number | undefined | null,
  digits = 2,
): string {
  if (n == null) return '—'
  return n.toLocaleString(undefined, {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  })
}

export const formatUsd = (n: number | undefined | null) => formatNum(n, 2)
export const formatBtc = (n: number | undefined | null) => formatNum(n, 8)

// Relative age of an epoch-ms instant, for staleness hints (e.g. mark
// price). Returns "12s ago" / "3m ago" / "1h ago"; em-dash for nullish.
export function formatAge(ms: number | undefined | null): string {
  if (ms == null) return '—'
  const sec = Math.max(0, Math.round((Date.now() - ms) / 1000))
  if (sec < 60) return `${sec}s ago`
  const min = Math.round(sec / 60)
  if (min < 60) return `${min}m ago`
  const hr = Math.round(min / 60)
  if (hr < 24) return `${hr}h ago`
  return `${Math.round(hr / 24)}d ago`
}
