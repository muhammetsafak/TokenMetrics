// Shared UI state: active view, filter, filter choices, scan state and the
// small loader every screen uses to fetch its data.
import { derived, get, writable, type Readable } from 'svelte/store'
import {
  api,
  errorMessage,
  onScanDone,
  onScanProgress,
  type Bucket,
  type Filter,
  type MetaData,
} from './api'
import * as scanReducer from './scanState'

// ---- navigation -----------------------------------------------------------

export type ViewId = 'overview' | 'compare' | 'projects' | 'limits' | 'data' | 'settings'

export const VIEWS: { id: ViewId; label: string }[] = [
  { id: 'overview', label: 'Özet' },
  { id: 'compare', label: 'Karşılaştırma' },
  { id: 'projects', label: 'Projeler' },
  { id: 'limits', label: 'Limitler' },
  { id: 'data', label: 'Veri' },
  { id: 'settings', label: 'Ayarlar' },
]

export const view = writable<ViewId>('overview')

// ---- filter ---------------------------------------------------------------

export type RangeKey = 'today' | '7d' | '30d' | '90d' | 'all' | 'custom'

export interface FilterState {
  range: RangeKey
  customFrom: string // yyyy-mm-dd, local
  customTo: string // inclusive
  services: string[]
  models: string[]
  sources: string[]
}

export const RANGES: { id: RangeKey; label: string }[] = [
  { id: 'today', label: 'Bugün' },
  { id: '7d', label: '7 gün' },
  { id: '30d', label: '30 gün' },
  { id: '90d', label: '90 gün' },
  { id: 'all', label: 'Tümü' },
  { id: 'custom', label: 'Özel' },
]

export const defaultFilter: FilterState = {
  range: '30d',
  customFrom: '',
  customTo: '',
  services: [],
  models: [],
  sources: [],
}

export const filter = writable<FilterState>({ ...defaultFilter })

/** Bumped when stored data changed (a scan finished); screens reload on it. */
export const dataVersion = writable(0)

function localMidnight(d: Date, addDays = 0): number {
  return new Date(d.getFullYear(), d.getMonth(), d.getDate() + addDays).getTime()
}

function parseDay(s: string): Date | null {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(s)
  if (!m) return null
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]))
}

/** True when the custom range is usable (both dates set, from <= to). */
export function customRangeValid(f: FilterState): boolean {
  const a = parseDay(f.customFrom)
  const b = parseDay(f.customTo)
  return a !== null && b !== null && a.getTime() <= b.getTime()
}

/** Time bounds of the range in local calendar days; 0 = open end. */
export function rangeBounds(f: FilterState, now: Date): { fromMs: number; toMs: number } {
  const tomorrow = localMidnight(now, 1)
  switch (f.range) {
    case 'today':
      return { fromMs: localMidnight(now), toMs: tomorrow }
    case '7d':
      return { fromMs: localMidnight(now, -6), toMs: tomorrow }
    case '30d':
      return { fromMs: localMidnight(now, -29), toMs: tomorrow }
    case '90d':
      return { fromMs: localMidnight(now, -89), toMs: tomorrow }
    case 'custom': {
      const a = parseDay(f.customFrom)
      const b = parseDay(f.customTo)
      if (a && b && a.getTime() <= b.getTime()) {
        return { fromMs: a.getTime(), toMs: localMidnight(b, 1) }
      }
      return { fromMs: 0, toMs: 0 }
    }
    default:
      return { fromMs: 0, toMs: 0 }
  }
}

/** The filter as the backend takes it. Recomputed after a scan so "today" rolls over. */
export const apiFilter: Readable<Filter> = derived([filter, dataVersion], ([$f]) => {
  const { fromMs, toMs } = rangeBounds($f, new Date())
  return {
    fromMs,
    toMs,
    sources: $f.sources,
    services: $f.services,
    models: $f.models,
    projects: [],
    sessions: [],
  }
})

/** Picks the time-series bucket for a range: short ranges by hour, long by month. */
export function bucketFor(fromMs: number, toMs: number, firstRecordMs: number, nowMs: number): Bucket {
  const from = fromMs || firstRecordMs || nowMs
  const to = toMs || nowMs
  const days = Math.max(0, (to - from) / 86_400_000)
  if (days <= 2) return 'hour'
  if (days <= 100) return 'day'
  if (days <= 730) return 'week'
  return 'month'
}

// ---- meta and scan --------------------------------------------------------

export const meta = writable<MetaData | null>(null)
export const metaError = writable('')

export async function refreshMeta(): Promise<void> {
  try {
    const m = await api.meta()
    meta.set(m)
    scan.update((s) => scanReducer.onMeta(s, m))
    metaError.set('')
  } catch (e) {
    metaError.set(errorMessage(e))
  }
}

export type { ScanState } from './scanState'

export const scan = writable<scanReducer.ScanState>(scanReducer.initialScanState)

/** Sources that failed or warned in the last scan, for the status line. */
export const scanIssues = derived(scan, ($s) => {
  const steps = $s.last?.steps ?? []
  return {
    errors: steps.filter((s) => s.status === 'error'),
    warnings: steps.reduce((n, s) => n + s.warnings.length, 0),
  }
})

export async function startScan(): Promise<void> {
  try {
    const id = await api.rescan()
    scan.update((s) => scanReducer.onStarted(s, id))
  } catch (e) {
    metaError.set(errorMessage(e))
  }
}

/** Wires the scan events and loads the first meta. Returns the cleanup function. */
export function initApp(): () => void {
  // Listeners first, then Meta. Whatever order the events, the Rescan answer
  // and the Meta snapshot arrive in, scanState reconciles them by scan id.
  const offProgress = onScanProgress((step) => {
    scan.update((s) => scanReducer.onProgress(s, step))
    if (step.status === 'done' && (step.added > 0 || step.updated > 0)) {
      dataVersion.update((n) => n + 1)
    }
  })
  // dataVersion reloads Meta (see below), so no extra refreshMeta here.
  const offDone = onScanDone((sum) => {
    scan.update((s) => scanReducer.onDone(s, sum))
    dataVersion.update((n) => n + 1)
  })
  void refreshMeta()
  return () => {
    offProgress()
    offDone()
  }
}

// Reload the filter choices whenever data changed.
dataVersion.subscribe(() => {
  if (get(meta) !== null) void refreshMeta()
})

// ---- loader ---------------------------------------------------------------

export interface Resource<T> {
  status: 'loading' | 'ready' | 'error'
  data: T | null // kept while a reload is in flight
  error: string
}

export interface Loader<T> extends Readable<Resource<T>> {
  /** Runs fn; a result that arrives after a newer load started is dropped. */
  load(fn: () => Promise<T>): Promise<void>
  /** Drops the data and any load in flight (e.g. when the subject changes). */
  reset(): void
}

export function createLoader<T>(): Loader<T> {
  const store = writable<Resource<T>>({ status: 'loading', data: null, error: '' })
  let seq = 0
  return {
    subscribe: store.subscribe,
    reset() {
      seq++
      store.set({ status: 'loading', data: null, error: '' })
    },
    async load(fn) {
      const mine = ++seq
      store.update((s) => ({ ...s, status: 'loading', error: '' }))
      try {
        const data = await fn()
        if (mine === seq) store.set({ status: 'ready', data, error: '' })
      } catch (e) {
        if (mine === seq) store.set({ status: 'error', data: null, error: errorMessage(e) })
      }
    },
  }
}

// ---- theme ----------------------------------------------------------------

/** Increments when the system color scheme changes so charts re-read the tokens. */
export const themeTick = writable(0)

if (typeof window !== 'undefined' && window.matchMedia) {
  window
    .matchMedia('(prefers-color-scheme: dark)')
    .addEventListener('change', () => themeTick.update((n) => n + 1))
}

// ---- labels ---------------------------------------------------------------

/** Source id -> display label, from Meta. */
export const sourceLabels: Readable<Record<string, string>> = derived(meta, ($m) => {
  const out: Record<string, string> = {}
  for (const s of $m?.sources ?? []) out[s.id] = s.label
  return out
})
