// Helpers for form inputs: date-time conversion and number checks.

const pad = (n: number) => String(n).padStart(2, '0')

/** Unix ms -> the value of a datetime-local input (local time, minute precision). */
export function msToLocalInput(ms: number): string {
  const d = new Date(ms)
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** The value of a datetime-local input -> unix ms; NaN when empty or invalid. */
export function localInputToMs(v: string): number {
  return v ? new Date(v).getTime() : NaN
}

/** Local calendar day (yyyy-mm-dd) of a date input -> local midnight in ms; NaN when empty. */
export function dayInputToMs(v: string, addDays = 0): number {
  const m = /^(\d{4})-(\d{2})-(\d{2})$/.exec(v)
  if (!m) return NaN
  return new Date(Number(m[1]), Number(m[2]) - 1, Number(m[3]) + addDays).getTime()
}

/** A number input is empty (null) or holds a finite number. */
export function isEmptyNumber(v: number | null | undefined): v is null | undefined {
  return v === null || v === undefined || Number.isNaN(v)
}

/** A non-negative whole number; empty counts as 0. */
export function wholeOrZero(v: number | null | undefined): number | null {
  if (isEmptyNumber(v)) return 0
  return Number.isInteger(v) && v >= 0 ? v : null
}
