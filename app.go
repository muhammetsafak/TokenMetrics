package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v2/pkg/runtime"

	appsvc "github.com/muhammetsafak/TokenMetrics/internal/app"
)

// Events the frontend listens to.
const (
	eventScanProgress = "scan:progress"
	eventScanDone     = "scan:done"
)

// shutdownScanWait bounds how long shutdown waits for a running scan.
const shutdownScanWait = 10 * time.Second

// App is the object Wails binds to the frontend. Every exported method is a
// binding; it only forwards to appsvc.Service and returns DTOs.
type App struct {
	ctx    context.Context
	cancel context.CancelFunc
	log    *slog.Logger

	svc     *appsvc.Service
	initErr error

	scans sync.WaitGroup
}

// NewApp creates a new App application struct
func NewApp() *App {
	return &App{log: slog.New(slog.NewTextHandler(os.Stderr, nil))}
}

// startup opens the service and starts the first scan in the background. An
// open failure is kept and returned by every binding so the UI can show it.
func (a *App) startup(ctx context.Context) {
	a.ctx, a.cancel = context.WithCancel(ctx)
	svc, err := appsvc.Open(a.ctx, a.log)
	if err != nil {
		a.log.Error("open service", "error", err)
		a.initErr = err
		return
	}
	a.svc = svc
	_, _ = a.startScan()
}

// shutdown cancels a running scan, waits for it and closes the database.
func (a *App) shutdown(_ context.Context) {
	if a.cancel != nil {
		a.cancel()
	}
	done := make(chan struct{})
	go func() { a.scans.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(shutdownScanWait):
		a.log.Warn("scan did not stop in time; closing anyway")
	}
	if a.svc != nil {
		if err := a.svc.Close(); err != nil {
			a.log.Error("close service", "error", err)
		}
	}
}

func (a *App) service() (*appsvc.Service, error) {
	if a.initErr != nil {
		return nil, a.initErr
	}
	if a.svc == nil {
		return nil, errors.New("service is not ready")
	}
	return a.svc, nil
}

// startScan reserves the scan slot and runs the scan in a goroutine, reporting
// it with events. It returns the id of the running scan and whether this call
// started it (false: a scan was already running, the id is that scan's).
//
// When the slot is held by a background refresh of one source, the full scan
// that was asked for is queued behind it (id 0, started false): answering
// with the refresh's id would report "done" for a scan that skipped the other
// sources.
func (a *App) startScan() (id int64, started bool) {
	id, started = a.svc.BeginScan()
	if !started {
		if a.svc.PartialScanRunning() {
			a.queueFullScan()
			return 0, false
		}
		return id, false
	}
	a.scans.Add(1)
	go func() {
		defer a.scans.Done()
		a.runScan(id)
	}()
	return id, true
}

// runScan runs the scan the caller reserved with BeginScan and reports it.
func (a *App) runScan(id int64) {
	// RunScan finishes the scan state before it returns, so Meta already
	// reports the scan as done when the event arrives.
	sum := a.svc.RunScan(a.ctx, id, func(step appsvc.ScanStep) {
		runtime.EventsEmit(a.ctx, eventScanProgress, step)
	})
	runtime.EventsEmit(a.ctx, eventScanDone, sum)
}

// queueFullScan starts a full scan as soon as the scan slot is free (see
// Service.RunQueuedFullScan).
func (a *App) queueFullScan() {
	a.scans.Add(1)
	go func() {
		defer a.scans.Done()
		a.svc.RunQueuedFullScan(a.ctx, a.runScan)
	}()
}

// Rescan starts a scan of all active sources and returns at once with the id
// of the running scan (the one it started, or the one already running); 0
// means the scan is queued behind a background refresh.
// Progress arrives as scan:progress events and the end as scan:done; every
// payload carries the scan id, so the caller can tell stale state from
// current state by comparing ids.
func (a *App) Rescan() (int64, error) {
	if _, err := a.service(); err != nil {
		return 0, err
	}
	id, _ := a.startScan()
	return id, nil
}

// Summary returns today / 7 day / 30 day totals and the selected range.
func (a *App) Summary(f appsvc.FilterDTO) (appsvc.SummaryDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.SummaryDTO{}, err
	}
	return svc.Summary(a.ctx, f)
}

// TimeSeries returns tokens per bucket (hour|day|week|month), optionally
// stacked by service, model or source.
func (a *App) TimeSeries(f appsvc.FilterDTO, bucket string, stackBy string) (appsvc.TimeSeriesDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.TimeSeriesDTO{}, err
	}
	return svc.TimeSeries(a.ctx, f, bucket, stackBy)
}

// Breakdown returns totals per service, model, project, session or source.
func (a *App) Breakdown(f appsvc.FilterDTO, dim string) (appsvc.BreakdownDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.BreakdownDTO{}, err
	}
	return svc.Breakdown(a.ctx, f, dim)
}

// Compare returns the service x model table.
func (a *App) Compare(f appsvc.FilterDTO) (appsvc.CompareDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.CompareDTO{}, err
	}
	return svc.Compare(a.ctx, f)
}

// Sessions returns the largest sessions matching the filter.
func (a *App) Sessions(f appsvc.FilterDTO, limit int) (appsvc.BreakdownDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.BreakdownDTO{}, err
	}
	return svc.Sessions(a.ctx, f, limit)
}

// Limits returns the configured limit windows and the current usage windows.
func (a *App) Limits() (appsvc.LimitsDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.LimitsDTO{}, err
	}
	return svc.Limits(a.ctx)
}

// Meta returns the filter choices and the scan state.
func (a *App) Meta() (appsvc.MetaDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.MetaDTO{}, err
	}
	return svc.Meta(a.ctx)
}

// ---- settings -------------------------------------------------------------
//
// Admin keys go in through SetAdminKey and never come back: every binding
// returns the masked state only.

// SourceSettings lists the sources with their switch, folder and last scan.
func (a *App) SourceSettings() ([]appsvc.SourceSettingDTO, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}
	return svc.SourceSettings(a.ctx)
}

// SetSourceEnabled switches a source on or off.
func (a *App) SetSourceEnabled(id string, enabled bool) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.SetSourceEnabled(a.ctx, id, enabled)
}

// PickSourcePath lets the user choose the log folder of a local source with a
// folder dialog. canceled is true when the dialog was closed without a choice.
// The frontend cannot send a path of its own.
func (a *App) PickSourcePath(id string) (canceled bool, err error) {
	svc, err := a.service()
	if err != nil {
		return false, err
	}
	rows, err := svc.SourceSettings(a.ctx)
	if err != nil {
		return false, err
	}
	opts := runtime.OpenDialogOptions{Title: "Günlük klasörünü seçin"}
	for _, r := range rows {
		if r.ID == id && r.PathExists {
			opts.DefaultDirectory = r.EffectivePath
		}
	}
	dir, err := runtime.OpenDirectoryDialog(a.ctx, opts)
	if err != nil {
		return false, err
	}
	if dir == "" {
		return true, nil
	}
	return false, svc.SetSourcePath(a.ctx, id, dir)
}

// ResetSourcePath goes back to the default log folder of a source.
func (a *App) ResetSourcePath(id string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.ResetSourcePath(a.ctx, id)
}

// AdminKeys returns the masked state of the stored Admin keys.
func (a *App) AdminKeys() (appsvc.AdminKeysDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.AdminKeysDTO{}, err
	}
	return svc.AdminKeys(), nil
}

// SetAdminKey stores an Admin key in the OS keychain and returns its masked state.
func (a *App) SetAdminKey(provider, key string) (appsvc.AdminKeyDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.AdminKeyDTO{}, err
	}
	return svc.SetAdminKey(provider, key)
}

// DeleteAdminKey removes a stored Admin key.
func (a *App) DeleteAdminKey(provider string) (appsvc.AdminKeyDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.AdminKeyDTO{}, err
	}
	return svc.DeleteAdminKey(provider)
}

// Prices returns the price table with the user's overrides applied.
func (a *App) Prices() (appsvc.PricesDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.PricesDTO{}, err
	}
	return svc.Prices(a.ctx)
}

// SetPriceOverride saves the user's price for a model.
func (a *App) SetPriceOverride(o appsvc.PriceOverrideDTO) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.SetPriceOverride(a.ctx, o)
}

// ResetPriceOverride removes the user's price of a model.
func (a *App) ResetPriceOverride(model string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.ResetPriceOverride(a.ctx, model)
}

// LimitSettings returns the configured limits and the weekly window start.
func (a *App) LimitSettings() (appsvc.LimitSettingsDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.LimitSettingsDTO{}, err
	}
	return svc.LimitSettings(a.ctx)
}

// SaveLimit creates or replaces the limit of a (service, kind) pair.
func (a *App) SaveLimit(l appsvc.LimitSettingDTO) (appsvc.LimitSettingDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.LimitSettingDTO{}, err
	}
	return svc.SaveLimit(a.ctx, l)
}

// DeleteLimit removes a limit.
func (a *App) DeleteLimit(id int64) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.DeleteLimit(a.ctx, id)
}

// SetWeeklyStart saves when the weekly window begins.
func (a *App) SetWeeklyStart(w appsvc.WeeklyStartDTO) (appsvc.WeeklyStartDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.WeeklyStartDTO{}, err
	}
	return svc.SetWeeklyStart(a.ctx, w)
}

// Budgets lists the budget rules.
func (a *App) Budgets() ([]appsvc.BudgetDTO, error) {
	svc, err := a.service()
	if err != nil {
		return nil, err
	}
	return svc.Budgets(a.ctx)
}

// SaveBudget creates (id 0) or replaces a budget rule.
func (a *App) SaveBudget(b appsvc.BudgetDTO) (appsvc.BudgetDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.BudgetDTO{}, err
	}
	return svc.SaveBudget(a.ctx, b)
}

// DeleteBudget removes a budget rule.
func (a *App) DeleteBudget(id int64) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.DeleteBudget(a.ctx, id)
}

// ---- data -----------------------------------------------------------------

// ManualRecords lists the hand-entered records, newest first.
func (a *App) ManualRecords() (appsvc.ManualListDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.ManualListDTO{}, err
	}
	return svc.ManualRecords(a.ctx)
}

// AddManualRecord stores a hand-entered record.
func (a *App) AddManualRecord(r appsvc.ManualRecordDTO) (appsvc.ManualRecordDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.ManualRecordDTO{}, err
	}
	return svc.AddManualRecord(a.ctx, r)
}

// UpdateManualRecord replaces a hand-entered record.
func (a *App) UpdateManualRecord(r appsvc.ManualRecordDTO) (appsvc.ManualRecordDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.ManualRecordDTO{}, err
	}
	return svc.UpdateManualRecord(a.ctx, r)
}

// DeleteManualRecord removes a hand-entered record.
func (a *App) DeleteManualRecord(externalID string) error {
	svc, err := a.service()
	if err != nil {
		return err
	}
	return svc.DeleteManualRecord(a.ctx, externalID)
}

// ImportCSV asks for a CSV file with a file dialog and imports it. The result
// has Canceled set when the dialog was closed. The frontend cannot send a path.
func (a *App) ImportCSV() (appsvc.ImportResultDTO, error) {
	return a.importFile(appsvc.FormatCSV, "CSV dosyası seçin", "CSV dosyaları (*.csv)", "*.csv")
}

// ImportJSON asks for a JSON export with a file dialog and merges it.
func (a *App) ImportJSON() (appsvc.ImportResultDTO, error) {
	return a.importFile(appsvc.FormatJSON, "JSON dışa aktarması seçin", "JSON dosyaları (*.json)", "*.json")
}

func (a *App) importFile(format, title, filterName, pattern string) (appsvc.ImportResultDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.ImportResultDTO{}, err
	}
	path, err := runtime.OpenFileDialog(a.ctx, runtime.OpenDialogOptions{
		Title:   title,
		Filters: []runtime.FileFilter{{DisplayName: filterName, Pattern: pattern}},
	})
	if err != nil {
		return appsvc.ImportResultDTO{}, err
	}
	if path == "" {
		return appsvc.ImportResultDTO{Canceled: true, Format: format, Errors: []appsvc.RowErrorDTO{}}, nil
	}
	if format == appsvc.FormatJSON {
		return svc.ImportJSONFile(a.ctx, path)
	}
	return svc.ImportCSVFile(a.ctx, path)
}

// ExportData asks where to save with a file dialog and writes the records
// matching f as "csv" or "json". The result has Canceled set when the dialog
// was closed.
func (a *App) ExportData(format string, f appsvc.FilterDTO) (appsvc.ExportResultDTO, error) {
	svc, err := a.service()
	if err != nil {
		return appsvc.ExportResultDTO{}, err
	}
	if format != appsvc.FormatCSV && format != appsvc.FormatJSON {
		return appsvc.ExportResultDTO{}, errors.New("bilinmeyen biçim")
	}
	path, err := runtime.SaveFileDialog(a.ctx, runtime.SaveDialogOptions{
		Title:           "Dışa aktarılacak dosyayı kaydedin",
		DefaultFilename: appsvc.ExportFileName(format, time.Now()),
		Filters:         []runtime.FileFilter{{DisplayName: strings.ToUpper(format) + " dosyaları (*." + format + ")", Pattern: "*." + format}},
	})
	if err != nil {
		return appsvc.ExportResultDTO{}, err
	}
	if path == "" {
		return appsvc.ExportResultDTO{Canceled: true, Format: format}, nil
	}
	return svc.ExportFile(a.ctx, path, format, f)
}
