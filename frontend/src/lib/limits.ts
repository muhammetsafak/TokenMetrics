// Pure helpers shared by the Limits view and the overview limit card.
import type { LimitItem } from './api'
import { formatPercent, formatTokens, formatUSD } from './format'

export type LevelId = 'ok' | 'near' | 'over' | 'unk'
export type Level = { id: LevelId; text: string }

/** A limit value in its metric's unit: dollars for cost_usd, K/M/B tokens otherwise. */
export const limitValue = (metric: string, v: number): string =>
  metric === 'cost_usd' ? formatUSD(v) : `${formatTokens(v)}`

/**
 * lowerBound: Used leaves out total-only tokens (see missingTotalOnlyTokens), so it
 * is a floor. A floor already at the limit is still certain; one below it proves nothing.
 */
export function limitLevel(ratio: number, lowerBound: boolean): Level {
  if (ratio >= 1) return { id: 'over', text: 'Limit aşıldı' }
  if (ratio >= 0.8) return { id: 'near', text: 'Limite yaklaşıyor' }
  if (lowerBound) return { id: 'unk', text: 'Durum belirsiz (alt sınır)' }
  return { id: 'ok', text: 'Limitin altında' }
}

/** Used value text: "≥ x" for a floor, "hesaplanamadı" when nothing could be counted. */
export function limitUsedText(l: LimitItem): string {
  if (l.missingTotalOnlyTokens <= 0) return limitValue(l.metric, l.used)
  return l.used === 0 ? 'hesaplanamadı' : `≥ ${limitValue(l.metric, l.used)}`
}

export function limitPercentText(l: LimitItem): string {
  if (l.missingTotalOnlyTokens <= 0) return formatPercent(l.ratio)
  return l.used === 0 ? '—' : `≥ ${formatPercent(l.ratio)}`
}
