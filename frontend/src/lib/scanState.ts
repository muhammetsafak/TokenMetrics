// Scan state as a pure reducer. Three sources report on a scan: the
// scan:progress / scan:done events, the answer of Rescan() and the snapshot in
// Meta(). They race (any of them can arrive late), so none of them is trusted
// by arrival order. Every payload carries the backend's scan id; a payload
// about a scan that is already known to be finished is ignored, and "scanning"
// is switched off only by evidence that the running scan finished.

import type { ScanStep, ScanSummary } from './api'

export interface ScanState {
  scanning: boolean
  /** Id of the newest scan known to be running (0 = none). */
  runningId: number
  /** Id of the newest scan known to be finished. */
  finishedId: number
  step: ScanStep | null
  last: ScanSummary | null
}

export const initialScanState: ScanState = {
  scanning: false,
  runningId: 0,
  finishedId: 0,
  step: null,
  last: null,
}

function markRunning(s: ScanState, id: number): ScanState {
  if (id <= 0 || id <= s.finishedId) return s // stale: that scan is over
  return { ...s, scanning: true, runningId: Math.max(s.runningId, id) }
}

/** A scan:progress event. */
export function onProgress(s: ScanState, step: ScanStep): ScanState {
  const next = markRunning(s, step.scanId)
  return next === s ? s : { ...next, step }
}

/** A scan:done event (also used for the summary inside a Meta snapshot). */
export function onDone(s: ScanState, sum: ScanSummary): ScanState {
  if (sum.id <= s.finishedId) return s
  const stillRunning = s.runningId > sum.id
  return {
    scanning: stillRunning,
    runningId: stillRunning ? s.runningId : 0,
    finishedId: sum.id,
    step: stillRunning ? s.step : null,
    last: sum,
  }
}

/** The id Rescan() returned: the scan it started or the one already running. */
export function onStarted(s: ScanState, id: number): ScanState {
  return markRunning(s, id)
}

export interface MetaScan {
  scanning: boolean
  scanId: number
  lastScan?: ScanSummary
}

/**
 * A Meta snapshot. It can switch scanning on (for a scan not known to be
 * finished) and it can report a finished scan, but a snapshot saying "idle"
 * never clears a running state by itself: it may have been read before the
 * scan started.
 */
export function onMeta(s: ScanState, m: MetaScan): ScanState {
  let next = s
  if (m.lastScan) next = onDone(next, m.lastScan)
  if (m.scanning) next = markRunning(next, m.scanId)
  return next
}
