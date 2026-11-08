// Cost display rules for total-only records (records that carry just a combined
// token count, no input/output split, so they cannot be priced). The backend
// flags this on Totals: costUnavailable (nothing in the set is priceable) and
// costPartial (costUsd is a lower bound).
import type { Totals } from './api'
import { formatInt, formatUSD } from './format'

export const COST_UNAVAILABLE_TEXT = 'hesaplanamadı'

/** Cell/label text of a cost: "$1.20", "≥ $1.20" (lower bound), "hesaplanamadı" or "—". */
export function costText(t: Totals): string {
  if (t.costUnavailable) return COST_UNAVAILABLE_TEXT
  if (t.costPartial && t.costUsd != null) return `≥ ${formatUSD(t.costUsd)}`
  return formatUSD(t.costUsd)
}

/** Tooltip that explains a not-computable or lower-bound cost; undefined for a plain one. */
export function costHint(t: Totals): string | undefined {
  if (t.costUnavailable) {
    return 'Bu kayıtlar yalnız toplam token içeriyor (girdi/çıktı ayrımı yok); fiyatlanamadığı için maliyet hesaplanamadı. Maliyet 0 $ değildir.'
  }
  if (t.costPartial) {
    return `Alt sınır: yalnız toplam token yazan kayıtların ${formatInt(t.uncostedTotalOnlyTokens)} token'ı fiyatlanamadı ve maliyete dahil değil.`
  }
  return undefined
}

/** Sort value of a cost column; a not-computable cost sorts last, like a missing one. */
export function costSortValue(t: Totals): number | null {
  return t.costUnavailable ? null : (t.costUsd ?? null)
}

/** Sum of the costs of several sets, with the same unavailable / lower-bound wording. */
export function costSumText(list: Totals[]): string {
  if (list.length > 0 && list.every((t) => t.costUnavailable)) return COST_UNAVAILABLE_TEXT
  const sum = list.reduce((n, t) => n + (t.costUnavailable ? 0 : (t.costUsd ?? 0)), 0)
  const lowerBound = list.some((t) => t.costUnavailable || t.costPartial)
  return lowerBound ? `≥ ${formatUSD(sum)}` : formatUSD(sum)
}

/** True when any of the sets has total-only records. */
export const hasTotalOnly = (list: Totals[]): boolean => list.some((t) => t.totalOnlyTokens > 0)
