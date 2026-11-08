// Chart colors come from the CSS tokens (style.css), so light and dark follow
// the system preference in one place. An entity keeps its color whatever the
// filter shows: the slot is its position in the unfiltered list from Meta.

const SLOTS = 8

export function cssVar(name: string): string {
  return getComputedStyle(document.documentElement).getPropertyValue(name).trim()
}

/**
 * Colors for the entities visible in one chart. An entity keeps the slot of its
 * position in the unfiltered key list (so a filter does not repaint it); an
 * entity that has no slot there (position >= 8) takes a slot nobody visible
 * uses, so it never shares a color with another layer or with "other". Only
 * when more than 8 entities are visible do the extra ones fall back to gray.
 */
export function assignColors(visible: string[], order: string[], otherKey = '__other__'): Map<string, string> {
  const colors = new Map<string, string>()
  const used = new Set<number>()
  const loose: string[] = []
  for (const key of visible) {
    if (key === otherKey) {
      colors.set(key, cssVar('--series-other'))
      continue
    }
    const i = order.indexOf(key)
    if (i >= 0 && i < SLOTS) {
      used.add(i)
      colors.set(key, cssVar(`--series-${i + 1}`))
    } else {
      loose.push(key)
    }
  }
  let next = 0
  for (const key of loose) {
    while (next < SLOTS && used.has(next)) next++
    if (next < SLOTS) {
      used.add(next)
      colors.set(key, cssVar(`--series-${next + 1}`))
    } else {
      colors.set(key, cssVar('--series-other'))
    }
  }
  return colors
}

export interface ChartTheme {
  text: string
  textSecondary: string
  grid: string
  axis: string
  surface: string
  accent: string
  font: string
  fontMono: string
}

/** Shared ECharts theme values, read from the CSS tokens so light and dark follow the system. */
export function chartTheme(): ChartTheme {
  return {
    text: cssVar('--text'),
    textSecondary: cssVar('--text-2'),
    grid: cssVar('--grid'),
    axis: cssVar('--axis'),
    surface: cssVar('--surface'),
    accent: cssVar('--series-1'),
    font: cssVar('--font'),
    fontMono: cssVar('--font-mono'),
  }
}
