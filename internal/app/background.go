package app

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/budget"
	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/ingest"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/notify"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

const (
	// SettingAPIRefreshMinutes is the settings key of the refresh period of
	// the API sources, in minutes. Absent: DefaultAPIRefresh. 0 switches the
	// background refresh of the API sources off. Values below
	// MinAPIRefresh are raised to it (API rate limits).
	SettingAPIRefreshMinutes = "background.api_refresh_minutes"
	// SettingBudgetNotifications is the settings key that switches the budget
	// notifications on or off. Absent: on.
	SettingBudgetNotifications = "notifications.budget_enabled"

	// DefaultAPIRefresh is the refresh period of the API sources.
	DefaultAPIRefresh = time.Hour
	// MinAPIRefresh is the shortest refresh period of the API sources.
	MinAPIRefresh = 5 * time.Minute
	// DefaultFileBackstop is the period of the backstop scan of the
	// file based sources; the file watcher normally runs them sooner.
	DefaultFileBackstop = 5 * time.Minute
	// DefaultStopTimeout is how long Stop waits before it gives up.
	DefaultStopTimeout = 10 * time.Second
)

// BackgroundConfig configures a Background. The zero value is usable (no
// notifications, no events, default timing).
type BackgroundConfig struct {
	// Notifier shows the budget notifications. Nil: no notifications.
	Notifier notify.Notifier
	// OnProgress and OnDone report the scans the background refresh runs, like
	// the scan:progress and scan:done events of a manual scan. Both may be nil.
	// OnDone is called for a finished scan only when there is news: data was
	// added or changed, or a source reported (or stopped reporting) a problem.
	// Quiet scans end silently; they are not reported as running either (see
	// Service.scanState), so no client waits for their end.
	OnProgress func(ScanStep)
	OnDone     func(ScanSummaryDTO)
	// OnBudgetAlert is called after every budget check with whether at least
	// one enabled budget has reached its threshold in its current period (the
	// tray shows its warning icon while this is true). It does not depend on
	// the notification setting or on a Notifier: it reports the state, not
	// the delivery. A check that failed does not call it. May be nil. It must
	// not block.
	OnBudgetAlert func(alert bool)
	// Scheduler tunes the debounce and retry delay (tests); its zero value
	// uses the defaults.
	Scheduler ingest.Config
	// FileBackstop overrides DefaultFileBackstop.
	FileBackstop time.Duration
	// StopTimeout bounds how long Stop waits for work that does not react to
	// cancellation (a blocked notification dialog, a stuck HTTP call).
	// Default DefaultStopTimeout.
	StopTimeout time.Duration
}

// Background keeps the data fresh while the application runs: it watches the
// log directories of the file based sources, refreshes the API sources on a
// timer and, after every scan, notifies about budgets that were breached.
type Background struct {
	svc   *Service
	cfg   BackgroundConfig
	sched *ingest.Scheduler

	mu      sync.Mutex
	cancel  context.CancelFunc
	started bool
	evalCh  chan struct{} // a scan ended: evaluate the budgets
	wg      sync.WaitGroup
}

// NewBackground returns a Background for svc.
func NewBackground(svc *Service, cfg BackgroundConfig) *Background {
	if cfg.FileBackstop <= 0 {
		cfg.FileBackstop = DefaultFileBackstop
	}
	if cfg.StopTimeout <= 0 {
		cfg.StopTimeout = DefaultStopTimeout
	}
	if cfg.Scheduler.Logger == nil {
		cfg.Scheduler.Logger = svc.log
	}
	return &Background{svc: svc, cfg: cfg, evalCh: make(chan struct{}, 1)}
}

// Start begins watching and scheduling. It returns once the file watches are
// in place; nothing is scanned by Start itself (the application runs the
// first scan). It ends with ctx or Stop.
func (b *Background) Start(ctx context.Context) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.started {
		return errors.New("app: background already started")
	}

	b.sched = ingest.NewScheduler(b.cfg.Scheduler)
	for _, c := range b.svc.registry.All() {
		task, ok := b.taskFor(c)
		if !ok {
			continue
		}
		if err := b.sched.Add(task); err != nil {
			return err
		}
	}

	ctx, b.cancel = context.WithCancel(ctx)
	if err := b.sched.Start(ctx); err != nil {
		b.cancel()
		return err
	}
	b.started = true

	// Every scan, wherever it was started from (startup, the Rescan binding,
	// the tray, this scheduler), ends in a budget check. The observer only
	// signals; the evaluation runs on its own goroutine so a scan never waits
	// for it. A scan that ended before the observer was installed is covered
	// by the initial signal.
	b.svc.SetScanObserver(func(ScanSummaryDTO) { b.signalEvaluate() })
	// A changed budget (added, edited, deleted) changes the alert state too.
	b.svc.SetBudgetStateObserver(b.signalEvaluate)
	b.wg.Add(1)
	go func() {
		defer b.wg.Done()
		b.evaluateLoop(ctx)
	}()
	b.signalEvaluate()
	return nil
}

// Stop ends the background work and waits for it, including a scan it
// started, for at most StopTimeout; on a timeout it logs a warning and
// returns (the work was canceled and ends on its own). It is safe to call
// more than once and without Start.
func (b *Background) Stop() {
	b.mu.Lock()
	cancel, sched := b.cancel, b.sched
	b.mu.Unlock()
	if cancel == nil {
		return
	}
	b.svc.SetScanObserver(nil)
	b.svc.SetBudgetStateObserver(nil)
	cancel()
	done := make(chan struct{})
	go func() {
		sched.Stop()
		b.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(b.cfg.StopTimeout):
		b.svc.log.Warn("background work did not stop in time; continuing shutdown")
	}
}

func (b *Background) signalEvaluate() {
	select {
	case b.evalCh <- struct{}{}:
	default:
	}
}

func (b *Background) evaluateLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.evalCh:
			if _, err := b.evaluateBudgets(ctx); err != nil && ctx.Err() == nil {
				b.svc.log.Warn("budget check failed", "error", err)
			}
		}
	}
}

// ---- tasks ----------------------------------------------------------------

// taskFor builds the scheduler task of a collector. Sources without a
// background mode (manual entry, CSV import have no collector) yield false.
func (b *Background) taskFor(c collector.Collector) (ingest.Task, bool) {
	id := c.Name()
	info := lookupSource(id)
	task := ingest.Task{
		Name: string(id),
		Run:  func(ctx context.Context) error { return b.runSource(ctx, id) },
	}
	switch info.kind {
	case "local":
		task.Roots = func(ctx context.Context) []string { return b.watchRoots(ctx, c) }
		backstop := b.cfg.FileBackstop
		task.Interval = func(context.Context) time.Duration { return backstop }
	case "api":
		task.Interval = b.apiInterval
	default:
		return ingest.Task{}, false
	}
	return task, true
}

// watchRoots returns the directory a file based source reads: the path set in
// the settings, else the collector's default. A disabled source is not
// watched.
func (b *Background) watchRoots(ctx context.Context, c collector.Collector) []string {
	id := c.Name()
	enabled, err := b.svc.sourceEnabled(ctx, id)
	if err != nil || !enabled {
		return nil
	}
	if key, ok := sourcePathKeys[id]; ok {
		var p string
		if found, err := b.svc.st.GetSetting(ctx, key, &p); err == nil && found && p != "" {
			return []string{p}
		}
	}
	if d, ok := c.(interface{ DefaultRoot() (string, error) }); ok {
		if root, err := d.DefaultRoot(); err == nil && root != "" {
			return []string{root}
		}
	}
	return nil
}

// apiInterval reads the refresh period of the API sources.
func (b *Background) apiInterval(ctx context.Context) time.Duration {
	var minutes int
	found, err := b.svc.st.GetSetting(ctx, SettingAPIRefreshMinutes, &minutes)
	if err != nil || !found {
		return DefaultAPIRefresh
	}
	return apiRefreshFromMinutes(minutes)
}

func apiRefreshFromMinutes(minutes int) time.Duration {
	if minutes <= 0 {
		return 0
	}
	d := time.Duration(minutes) * time.Minute
	if d < MinAPIRefresh {
		return MinAPIRefresh
	}
	return d
}

// runSource scans one source. It answers ingest.ErrBusy while another scan
// holds the scan slot, so the scheduler retries shortly.
func (b *Background) runSource(ctx context.Context, id model.Source) error {
	active, _, err := b.svc.sourceActive(ctx, lookupSource(id))
	if err != nil {
		return err
	}
	if !active {
		return nil // disabled, or an API source without a key
	}
	prevFailed := b.svc.lastStepFailed(id)
	scanID, ok := b.svc.beginScan(true)
	if !ok {
		return ingest.ErrBusy
	}
	sum := b.svc.RunScanSources(ctx, scanID, []model.Source{id}, b.cfg.OnProgress)
	// Tell the UI only when there is something to show: new data, or a
	// problem (or the end of one). Scans that found nothing are silent, so a
	// log that is written all day does not make the dashboard reload all day.
	// (They are not reported as running either, see scanState.)
	if b.cfg.OnDone != nil && ctx.Err() == nil && (scanChanged(sum) || prevFailed) {
		b.cfg.OnDone(sum)
	}
	return nil
}

// scanChanged reports whether a scan changed data or ran into a problem.
func scanChanged(sum ScanSummaryDTO) bool {
	if sum.Added > 0 || sum.Updated > 0 {
		return true
	}
	for _, st := range sum.Steps {
		if st.ScanID == sum.ID && (st.Error != "" || len(st.Warnings) > 0) {
			return true // steps of other sources are carried over, not news
		}
	}
	return false
}

// lastStepFailed reports whether the last scan left an error or warnings for
// the source, so the scan that clears them is reported too.
func (s *Service) lastStepFailed(id model.Source) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last == nil {
		return false
	}
	for _, st := range s.last.Steps {
		if st.Source == string(id) {
			return st.Error != "" || len(st.Warnings) > 0
		}
	}
	return false
}

// queuePoll is how often a queued full scan looks for a free scan slot. A
// variable so tests can shorten it.
var queuePoll = 200 * time.Millisecond

// RunQueuedFullScan waits until the scan slot is free, reserves it and calls
// run with the scan id; run must end the scan (RunScan). It is for a manual
// "scan everything" request that arrives while a background refresh of one
// source holds the slot: that refresh must not stand in for the full scan.
//
// Only one request waits at a time: while one is queued, further calls return
// false at once. It also returns false when ctx ends before the slot was free.
func (s *Service) RunQueuedFullScan(ctx context.Context, run func(id int64)) (ran bool) {
	s.mu.Lock()
	if s.fullQueued {
		s.mu.Unlock()
		return false
	}
	s.fullQueued = true
	s.mu.Unlock()
	unqueue := func() {
		s.mu.Lock()
		s.fullQueued = false
		s.mu.Unlock()
	}

	tick := time.NewTicker(queuePoll)
	defer tick.Stop()
	for {
		if id, ok := s.BeginScan(); ok {
			unqueue() // a new request may queue again while this scan runs
			run(id)
			return true
		}
		select {
		case <-ctx.Done():
			unqueue()
			return false
		case <-tick.C:
		}
	}
}

// PartialScanRunning reports whether the running scan covers only some
// sources (a background refresh).
func (s *Service) PartialScanRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanning && s.partial
}

// SetScanObserver registers fn to be called after every scan that was not
// canceled, on the scan's goroutine and after the service reports the scan as
// finished. nil removes the observer. fn must not block.
func (s *Service) SetScanObserver(fn func(ScanSummaryDTO)) {
	s.mu.Lock()
	s.observer = fn
	s.mu.Unlock()
}

// filterCollectors keeps the collectors whose source is in only, in order.
func filterCollectors(all []collector.Collector, only []model.Source) []collector.Collector {
	keep := make(map[model.Source]bool, len(only))
	for _, id := range only {
		keep[id] = true
	}
	out := make([]collector.Collector, 0, len(only))
	for _, c := range all {
		if keep[c.Name()] {
			out = append(out, c)
		}
	}
	return out
}

// mergeSteps returns one step per registered source, in registry order: the
// fresh step when the source ran, else the step of the previous scan (a
// source that never ran has none).
func mergeSteps(prev *ScanSummaryDTO, fresh []ScanStep, all []collector.Collector) []ScanStep {
	byKey := map[string]ScanStep{}
	if prev != nil {
		for _, st := range prev.Steps {
			byKey[st.Source] = st
		}
	}
	for _, st := range fresh {
		byKey[st.Source] = st
	}
	out := make([]ScanStep, 0, len(byKey))
	for _, c := range all {
		if st, ok := byKey[string(c.Name())]; ok {
			out = append(out, st)
		}
	}
	return out
}

// ---- budget notifications -------------------------------------------------

// evaluateBudgets notifies about every budget that reached its threshold in
// the current period and was not notified yet, and returns how many
// notifications were delivered. A breach is marked as notified only after the
// notifier accepted the message, so a failed delivery is retried after the
// next scan; a delivered one never repeats within its period.
//
// It also reports, through OnBudgetAlert, whether any budget is over its
// threshold right now. That part runs even when the notifications are off or
// there is no Notifier.
func (b *Background) evaluateBudgets(ctx context.Context) (int, error) {
	if b.cfg.Notifier == nil && b.cfg.OnBudgetAlert == nil {
		return 0, nil
	}
	enabled := true
	if _, err := b.svc.st.GetSetting(ctx, SettingBudgetNotifications, &enabled); err != nil {
		return 0, err
	}
	notifying := enabled && b.cfg.Notifier != nil
	if !notifying && b.cfg.OnBudgetAlert == nil {
		return 0, nil
	}
	p, err := b.svc.pricer(ctx)
	if err != nil {
		return 0, err
	}
	ev := budget.New(b.svc.st, p, b.svc.loc)
	now := b.svc.now()
	breaches, err := ev.Evaluate(ctx, now)
	if err != nil {
		return 0, err
	}
	if b.cfg.OnBudgetAlert != nil {
		// Evaluate leaves out the budgets that were already notified for the
		// period. Those can be under their threshold again (a record was
		// deleted, a price corrected), so they are measured anew (Status)
		// rather than assumed to be over it.
		alert := len(breaches) > 0
		if !alert {
			over, err := ev.Status(ctx, now)
			if err != nil {
				return 0, err
			}
			alert = len(over) > 0
		}
		b.cfg.OnBudgetAlert(alert)
	}
	if !notifying {
		return 0, nil
	}
	sent := 0
	var errs []error
	for _, br := range breaches {
		if err := b.cfg.Notifier.Notify(ctx, breachMessage(br)); err != nil {
			errs = append(errs, fmt.Errorf("notify budget %d: %w", br.BudgetID, err))
			continue
		}
		sent++
		if err := ev.MarkNotified(ctx, br, now); err != nil {
			errs = append(errs, fmt.Errorf("mark budget %d notified: %w", br.BudgetID, err))
		}
	}
	return sent, errors.Join(errs...)
}

// breachMessage words a breach (user facing text is Turkish).
func breachMessage(br budget.Breach) notify.Message {
	period := "Günlük"
	if br.Period == store.BudgetPeriodMonthly {
		period = "Aylık"
	}
	scope := "toplam"
	switch br.Scope {
	case store.BudgetScopeService, store.BudgetScopeModel:
		scope = br.ScopeValue
	}

	var parts []string
	if br.TokensExceeded && br.ThresholdTokens != nil {
		parts = append(parts, fmt.Sprintf("%s / %s token", formatTokens(br.UsedTokens), formatTokens(*br.ThresholdTokens)))
	}
	if br.USDExceeded && br.ThresholdUSD != nil {
		usd := fmt.Sprintf("%s / %s", formatUSD(br.UsedUSD), formatUSD(*br.ThresholdUSD))
		if br.Estimated {
			usd += " (tahmini)"
		}
		parts = append(parts, usd)
	}
	return notify.Message{
		ID:    fmt.Sprintf("budget-%d-%s", br.BudgetID, br.PeriodKey),
		Title: "Bütçe aşıldı",
		Body:  fmt.Sprintf("%s bütçe (%s): %s", period, scope, strings.Join(parts, ", ")),
	}
}

// formatTokens prints a token count compactly (Turkish units): 950,
// 12,5 bin, 3,2 milyon, 1,1 milyar.
func formatTokens(n int64) string {
	f := float64(n)
	switch {
	case n >= 1_000_000_000:
		return trimDecimal(f/1e9) + " milyar"
	case n >= 1_000_000:
		return trimDecimal(f/1e6) + " milyon"
	case n >= 1_000:
		return trimDecimal(f/1e3) + " bin"
	}
	return fmt.Sprintf("%d", n)
}

func trimDecimal(v float64) string {
	s := fmt.Sprintf("%.1f", v)
	s = strings.TrimSuffix(s, ".0")
	return strings.Replace(s, ".", ",", 1)
}

func formatUSD(v float64) string {
	return "$" + strings.Replace(fmt.Sprintf("%.2f", v), ".", ",", 1)
}
