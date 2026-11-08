// Typed access to the Go bindings (generated into ../../wailsjs) and the scan
// events. The DTO types are the generated ones; scan event payloads are not
// referenced by a binding directly, but they reach the generated models through Meta.
import {
  AddManualRecord,
  AdminKeys,
  Breakdown,
  Budgets,
  Compare,
  DeleteAdminKey,
  DeleteBudget,
  DeleteLimit,
  DeleteManualRecord,
  ExportData,
  ImportCSV,
  ImportJSON,
  LimitSettings,
  Limits,
  ManualRecords,
  Meta,
  PickSourcePath,
  Prices,
  Rescan,
  ResetPriceOverride,
  ResetSourcePath,
  SaveBudget,
  SaveLimit,
  Sessions,
  SetAdminKey,
  SetPriceOverride,
  SetSourceEnabled,
  SetWeeklyStart,
  SourceSettings,
  Summary,
  TimeSeries,
  UpdateManualRecord,
} from '../../wailsjs/go/main/App'
import { EventsOn } from '../../wailsjs/runtime/runtime'
import type { app } from '../../wailsjs/go/models'

export type Filter = app.FilterDTO
export type Totals = app.TotalsDTO
export type SummaryData = app.SummaryDTO
export type TimeSeriesData = app.TimeSeriesDTO
export type BreakdownData = app.BreakdownDTO
export type BreakdownRow = app.BreakdownRowDTO
export type CompareData = app.CompareDTO
export type CompareRow = app.CompareRowDTO
export type LimitsData = app.LimitsDTO
export type LimitItem = app.LimitDTO
export type CurrentWindow = app.CurrentWindowDTO
export type MetaData = app.MetaDTO
export type SourceInfo = app.SourceDTO

export type SourceSetting = app.SourceSettingDTO
export type AdminKeysData = app.AdminKeysDTO
export type AdminKeyState = app.AdminKeyDTO
export type PricesData = app.PricesDTO
export type PriceRow = app.PriceRowDTO
export type PriceSet = app.PriceSetDTO
export type PriceOverride = app.PriceOverrideDTO
export type LimitSettingsData = app.LimitSettingsDTO
export type LimitSetting = app.LimitSettingDTO
export type WeeklyStart = app.WeeklyStartDTO
export type Budget = app.BudgetDTO
export type ManualRecord = app.ManualRecordDTO
export type ManualList = app.ManualListDTO
export type ImportResult = app.ImportResultDTO
export type ExportResult = app.ExportResultDTO

export type ScanStep = app.ScanStep
export type ScanSummary = app.ScanSummaryDTO

export type Dimension = 'service' | 'model' | 'project' | 'session' | 'source'
export type Bucket = 'hour' | 'day' | 'week' | 'month'
export type StackBy = '' | 'service' | 'model' | 'source'

export const api = {
  summary: (f: Filter) => Summary(f),
  timeSeries: (f: Filter, bucket: Bucket, stackBy: StackBy) => TimeSeries(f, bucket, stackBy),
  breakdown: (f: Filter, dim: Dimension) => Breakdown(f, dim),
  compare: (f: Filter) => Compare(f),
  sessions: (f: Filter, limit: number) => Sessions(f, limit),
  limits: () => Limits(),
  meta: () => Meta(),
  /** Starts a scan and returns its id (or the id of the scan already running). */
  rescan: () => Rescan(),

  // Settings. No binding takes a file path: folders and files are chosen in
  // native dialogs opened by the backend.
  sourceSettings: () => SourceSettings(),
  setSourceEnabled: (id: string, enabled: boolean) => SetSourceEnabled(id, enabled),
  /** Opens a folder dialog; resolves true when the user closed it without choosing. */
  pickSourcePath: (id: string) => PickSourcePath(id),
  resetSourcePath: (id: string) => ResetSourcePath(id),
  adminKeys: () => AdminKeys(),
  /** The key goes in and is never returned; the answer holds the masked state only. */
  setAdminKey: (provider: string, key: string) => SetAdminKey(provider, key),
  deleteAdminKey: (provider: string) => DeleteAdminKey(provider),
  prices: () => Prices(),
  setPriceOverride: (o: PriceOverride) => SetPriceOverride(o),
  resetPriceOverride: (model: string) => ResetPriceOverride(model),
  limitSettings: () => LimitSettings(),
  saveLimit: (l: LimitSetting) => SaveLimit(l),
  deleteLimit: (id: number) => DeleteLimit(id),
  setWeeklyStart: (w: WeeklyStart) => SetWeeklyStart(w),
  budgets: () => Budgets(),
  saveBudget: (b: Budget) => SaveBudget(b),
  deleteBudget: (id: number) => DeleteBudget(id),

  // Data.
  manualRecords: () => ManualRecords(),
  addManualRecord: (r: ManualRecord) => AddManualRecord(r),
  updateManualRecord: (r: ManualRecord) => UpdateManualRecord(r),
  deleteManualRecord: (id: string) => DeleteManualRecord(id),
  /** Opens a file dialog; the result has `canceled` set when it was closed. */
  importCsv: () => ImportCSV(),
  importJson: () => ImportJSON(),
  exportData: (format: 'csv' | 'json', f: Filter) => ExportData(format, f),
}

/** Subscribes to scan progress; returns the unsubscribe function. */
export function onScanProgress(cb: (step: ScanStep) => void): () => void {
  return EventsOn('scan:progress', (step: ScanStep) => cb(step))
}

/** Subscribes to the end of a scan; returns the unsubscribe function. */
export function onScanDone(cb: (sum: ScanSummary) => void): () => void {
  return EventsOn('scan:done', (sum: ScanSummary) => cb(sum))
}

export function errorMessage(e: unknown): string {
  if (typeof e === 'string') return e
  if (e instanceof Error) return e.message
  return 'Bilinmeyen hata'
}
