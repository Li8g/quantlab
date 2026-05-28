// Epoch-ms formatting. All API timestamps are unix milliseconds.

export function formatMs(ms: number | undefined | null): string {
  if (ms == null) return '—'
  return new Date(ms).toLocaleString()
}
