// Number and date formatting. Numbers use K/M/B suffixes ("1.2M"); dates are
// shown in the machine's local time zone.

const intFmt = new Intl.NumberFormat('tr-TR')
const usdFmt = new Intl.NumberFormat('en-US', {
  style: 'currency',
  currency: 'USD',
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
})

/** 1234 -> "1.2K", 1_200_000 -> "1.2M". Values under 1000 stay exact. */
export function formatTokens(n: number): string {
  const abs = Math.abs(n)
  const units = ['K', 'M', 'B']
  if (abs < 1000) return String(Math.round(n))
  let idx = Math.min(units.length - 1, Math.floor(Math.log10(abs) / 3) - 1)
  let v = n / 1000 ** (idx + 1)
  // 999.96K rounds to "1000K": roll over to the next unit.
  if (Math.abs(Number(v.toFixed(1))) >= 1000 && idx < units.length - 1) {
    idx++
    v = n / 1000 ** (idx + 1)
  }
  return `${trimZero(v.toFixed(Math.abs(v) >= 100 ? 0 : 1))}${units[idx]}`
}

function trimZero(s: string): string {
  return s.includes('.') ? s.replace(/\.?0+$/, '') : s
}

/** Full number with locale grouping: 1.234.567. */
export function formatInt(n: number): string {
  return intFmt.format(Math.round(n))
}

/** "$12.34"; amounts under one cent show "<$0.01". */
export function formatUSD(n: number | undefined | null): string {
  if (n === undefined || n === null) return '—'
  if (n > 0 && n < 0.01) return '<$0.01'
  return usdFmt.format(n)
}

export function formatPercent(ratio: number): string {
  return `${Math.round(ratio * 100)}%`
}

const dateTimeFmt = new Intl.DateTimeFormat('tr-TR', {
  day: 'numeric',
  month: 'short',
  hour: '2-digit',
  minute: '2-digit',
})
const dateFmt = new Intl.DateTimeFormat('tr-TR', { day: 'numeric', month: 'short', year: 'numeric' })
const dayShortFmt = new Intl.DateTimeFormat('tr-TR', { day: 'numeric', month: 'short' })
const monthFmt = new Intl.DateTimeFormat('tr-TR', { month: 'short', year: 'numeric' })
const hourFmt = new Intl.DateTimeFormat('tr-TR', { hour: '2-digit', minute: '2-digit' })

export function formatDateTime(ms: number): string {
  return dateTimeFmt.format(new Date(ms))
}

export function formatDate(ms: number): string {
  return dateFmt.format(new Date(ms))
}

/**
 * Axis/tooltip label of a time bucket start. withDate adds the day to an hour
 * label, needed when the axis spans more than one day.
 */
export function formatBucket(ms: number, bucket: string, withDate = false): string {
  const d = new Date(ms)
  switch (bucket) {
    case 'hour':
      return withDate ? dateTimeFmt.format(d) : hourFmt.format(d)
    case 'month':
      return monthFmt.format(d)
    case 'week':
      return `${dayShortFmt.format(d)} haftası`
    default:
      return dayShortFmt.format(d)
  }
}

/** "5 dk önce", "dün 14:20" style: coarse relative time for the last scan. */
export function formatAgo(ms: number, now = Date.now()): string {
  const sec = Math.max(0, Math.round((now - ms) / 1000))
  if (sec < 45) return 'az önce'
  const min = Math.round(sec / 60)
  if (min < 60) return `${min} dk önce`
  const hour = Math.round(min / 60)
  if (hour < 24) return `${hour} sa önce`
  return formatDateTime(ms)
}

const SERVICE_LABELS: Record<string, string> = {
  anthropic: 'Anthropic',
  openai: 'OpenAI',
  google: 'Google',
}

export function serviceLabel(key: string): string {
  if (key === '') return '(servis yok)'
  return SERVICE_LABELS[key] ?? key
}

export function modelLabel(key: string): string {
  return key === '' ? '(model yok)' : key
}

/** Project paths are long; the last two segments identify them well enough. */
export function projectLabel(key: string): string {
  if (key === '') return '(proje yok)'
  const parts = key.split(/[\\/]+/).filter(Boolean)
  return parts.length <= 2 ? key : `…/${parts.slice(-2).join('/')}`
}

export function sessionLabel(key: string): string {
  if (key === '') return '(oturum yok)'
  return key.length > 12 ? `${key.slice(0, 8)}…` : key
}

export const LIMIT_KIND_LABELS: Record<string, string> = {
  '5h': '5 saatlik pencere',
  weekly: 'Haftalık pencere',
}

export const METRIC_LABELS: Record<string, string> = {
  total_tokens: 'toplam token',
  output_tokens: 'çıktı token',
  cost_usd: 'tahmini maliyet',
}

export function escapeHtml(s: string): string {
  return s.replace(/[&<>"']/g, (c) => `&#${c.charCodeAt(0)};`)
}

/** Display label of a breakdown key for a dimension. */
export function dimensionLabel(dim: string, key: string, sourceLabels: Record<string, string> = {}): string {
  switch (dim) {
    case 'service':
      return serviceLabel(key)
    case 'model':
      return modelLabel(key)
    case 'project':
      return projectLabel(key)
    case 'session':
      return sessionLabel(key)
    case 'source':
      return sourceLabels[key] ?? key
    default:
      return key
  }
}

/** True when the bucket starts fall on more than one local calendar day. */
export function spansDays(buckets: number[]): boolean {
  if (buckets.length < 2) return false
  const first = new Date(buckets[0])
  const last = new Date(buckets[buckets.length - 1])
  return first.toDateString() !== last.toDateString()
}

/** IANA name of the time zone the UI renders dates in (the system zone). */
export function localTimeZone(): string {
  return Intl.DateTimeFormat().resolvedOptions().timeZone
}
