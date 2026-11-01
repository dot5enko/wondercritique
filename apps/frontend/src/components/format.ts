/** Small date/number formatting helpers shared by studio components. */

/** "just now", "3m ago", "2h ago", "5d ago", or a locale date beyond that. */
export function relativeTime(iso: string | null | undefined): string {
  if (!iso) return '—'
  const t = new Date(iso).getTime()
  if (!Number.isFinite(t)) return '—'
  const diff = Date.now() - t
  if (diff < 45_000) return 'just now'
  const minutes = Math.round(diff / 60_000)
  if (minutes < 60) return `${minutes}m ago`
  const hours = Math.round(minutes / 60)
  if (hours < 24) return `${hours}h ago`
  const days = Math.round(hours / 24)
  if (days < 30) return `${days}d ago`
  return new Date(t).toLocaleDateString()
}

/** Absolute timestamp for tooltips/titles, e.g. "Nov 1, 2026, 3:24 PM". */
export function absoluteTime(iso: string | null | undefined): string {
  if (!iso) return ''
  const d = new Date(iso)
  return Number.isFinite(d.getTime()) ? d.toLocaleString() : ''
}
