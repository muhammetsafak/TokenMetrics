package app

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/budget"
	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/ingest"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/notify"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// bgCollector is a collector that counts its runs.
type bgCollector struct {
	name  model.Source
	calls atomic.Int32
	run   func(ctx context.Context) (collector.Result, error)
}

func (c *bgCollector) Name() model.Source { return c.name }
func (c *bgCollector) Collect(ctx context.Context, _ collector.Deps) (collector.Result, error) {
	c.calls.Add(1)
	if c.run != nil {
		return c.run(ctx)
	}
	return collector.Result{Added: 1, Files: 1}, nil
}

// fakeNotifier records the notifications it was asked to show.
type fakeNotifier struct {
	mu   sync.Mutex
	got  []notify.Message
	fail error
}

func (n *fakeNotifier) Notify(_ context.Context, m notify.Message) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.fail != nil {
		return n.fail
	}
	n.got = append(n.got, m)
	return nil
}

func (n *fakeNotifier) messages() []notify.Message {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]notify.Message(nil), n.got...)
}

func (n *fakeNotifier) setFail(err error) {
	n.mu.Lock()
	n.fail = err
	n.mu.Unlock()
}

// newBgService returns a service whose API key check says yes, with the clock
// the test controls.
func newBgService(t *testing.T, st *store.Store, now func() time.Time, cols ...collector.Collector) *Service {
	t.Helper()
	reg := collector.NewRegistry()
	for _, c := range cols {
		if err := reg.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{
		Store: st, DeviceID: "dev", Registry: reg,
		Now: now, Location: testLoc,
		HasKey: func(string) (bool, error) { return true, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func fixedNow() time.Time { return testNow }

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func stays(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if cond() {
			t.Fatalf("unexpected: %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func fastBg() BackgroundConfig {
	return BackgroundConfig{Scheduler: ingest.Config{Debounce: 40 * time.Millisecond, RetryDelay: 30 * time.Millisecond}}
}

func startBg(t *testing.T, svc *Service, cfg BackgroundConfig) *Background {
	t.Helper()
	b := NewBackground(svc, cfg)
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Stop)
	return b
}

func tokenBudget(t *testing.T, st *store.Store, period string, tokens int64) int64 {
	t.Helper()
	id, err := st.CreateBudget(context.Background(), store.Budget{
		Scope: store.BudgetScopeTotal, Period: period, ThresholdTokens: &tokens, Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// ---- file watching ----------------------------------------------------------

func TestBackgroundFileChangeRunsOnlyThatSource(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root := t.TempDir()
	if err := st.SetSetting(ctx, "sources.claude_code.path", root); err != nil {
		t.Fatal(err)
	}
	cc := &bgCollector{name: model.SourceClaudeCode}
	codex := &bgCollector{name: model.SourceCodexCLI}
	api := &bgCollector{name: model.SourceAnthropicAPI}
	svc := newBgService(t, st, fixedNow, cc, codex, api)

	var mu sync.Mutex
	var done []ScanSummaryDTO
	startBg(t, svc, BackgroundConfig{
		Scheduler: fastBg().Scheduler,
		OnDone: func(s ScanSummaryDTO) {
			mu.Lock()
			done = append(done, s)
			mu.Unlock()
		},
	})

	// Nothing runs on its own right after start: the app does the first scan.
	stays(t, "a run before any change", 150*time.Millisecond, func() bool { return cc.calls.Load() > 0 })

	// A burst of writes is one run of that source only.
	log := filepath.Join(root, "project-a", "session.jsonl")
	if err := os.MkdirAll(filepath.Dir(log), 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		f, err := os.OpenFile(log, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("{}\n")
		_ = f.Close()
	}
	waitUntil(t, "the Claude Code run", func() bool { return cc.calls.Load() >= 1 })
	waitUntil(t, "the done report", func() bool { mu.Lock(); defer mu.Unlock(); return len(done) >= 1 })
	stays(t, "a second run for one burst", 200*time.Millisecond, func() bool { return cc.calls.Load() > 1 })

	if codex.calls.Load() != 0 || api.calls.Load() != 0 {
		t.Fatalf("other sources ran: codex=%d api=%d", codex.calls.Load(), api.calls.Load())
	}
	mu.Lock()
	defer mu.Unlock()
	sum := done[0]
	if sum.Added != 1 || sum.Canceled {
		t.Fatalf("summary = %+v", sum)
	}
	// Steps of sources that did not run are not part of the summary until
	// they ran once; the one that ran is.
	if len(sum.Steps) != 1 || sum.Steps[0].Source != string(model.SourceClaudeCode) || sum.Steps[0].Status != StatusDone {
		t.Fatalf("steps = %+v", sum.Steps)
	}
	// The scan state of the service moved on like for any other scan.
	if sum.ID != 1 || svc.Scanning() {
		t.Fatalf("scan id %d scanning %v", sum.ID, svc.Scanning())
	}
}

func TestBackgroundSkipsInactiveSources(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root := t.TempDir()
	if err := st.SetSetting(ctx, "sources.claude_code.path", root); err != nil {
		t.Fatal(err)
	}
	if err := st.SetSetting(ctx, "sources.claude_code.enabled", false); err != nil {
		t.Fatal(err)
	}
	cc := &bgCollector{name: model.SourceClaudeCode}
	svc := newBgService(t, st, fixedNow, cc)
	startBg(t, svc, fastBg())

	// A disabled source is not even watched; a manual trigger of its task is
	// a no-op that does not consume a scan id.
	if err := os.WriteFile(filepath.Join(root, "x.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stays(t, "a run of a disabled source", 300*time.Millisecond, func() bool { return cc.calls.Load() > 0 })
	if id, ok := svc.BeginScan(); !ok || id != 1 {
		t.Fatalf("first scan id = %d (ok %v), want 1: the background must not have started a scan", id, ok)
	}
}

func TestBackgroundWaitsForTheScanSlot(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root := t.TempDir()
	if err := st.SetSetting(ctx, "sources.claude_code.path", root); err != nil {
		t.Fatal(err)
	}
	cc := &bgCollector{name: model.SourceClaudeCode}
	svc := newBgService(t, st, fixedNow, cc)
	startBg(t, svc, fastBg())

	// A manual scan holds the slot.
	held, ok := svc.BeginScan()
	if !ok {
		t.Fatal("slot busy")
	}
	if err := os.WriteFile(filepath.Join(root, "x.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	stays(t, "a run while the slot is held", 300*time.Millisecond, func() bool { return cc.calls.Load() > 0 })

	// When the manual scan ends, the background run follows by itself.
	svc.RunScan(ctx, held, nil)
	manualRuns := cc.calls.Load() // the manual scan ran Claude Code once
	waitUntil(t, "the background run after the slot was freed", func() bool { return cc.calls.Load() > manualRuns })
}

func TestAPIRefreshSetting(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	b := NewBackground(newBgService(t, st, fixedNow), fastBg())

	if got := b.apiInterval(ctx); got != time.Hour {
		t.Fatalf("default = %v, want 1h", got)
	}
	for _, c := range []struct {
		minutes int
		want    time.Duration
	}{
		{30, 30 * time.Minute},
		{1, MinAPIRefresh},
		{-5, 0},
		{0, 0},
	} {
		if err := st.SetSetting(ctx, SettingAPIRefreshMinutes, c.minutes); err != nil {
			t.Fatal(err)
		}
		if got := b.apiInterval(ctx); got != c.want {
			t.Errorf("setting %d min -> %v, want %v", c.minutes, got, c.want)
		}
	}
}

func TestBackgroundTasksPerSourceKind(t *testing.T) {
	st := openStore(t)
	svc := newBgService(t, st, fixedNow,
		&bgCollector{name: model.SourceClaudeCode},
		&bgCollector{name: model.SourceAnthropicAPI},
	)
	b := NewBackground(svc, BackgroundConfig{})
	var local, api ingest.Task
	for _, c := range svc.registry.All() {
		task, ok := b.taskFor(c)
		if !ok {
			t.Fatalf("no task for %s", c.Name())
		}
		if c.Name() == model.SourceClaudeCode {
			local = task
		} else {
			api = task
		}
	}
	ctx := context.Background()
	if local.Roots == nil || local.Interval(ctx) != DefaultFileBackstop {
		t.Errorf("local task: roots nil=%v interval=%v", local.Roots == nil, local.Interval(ctx))
	}
	if api.Roots != nil || api.Interval(ctx) != DefaultAPIRefresh {
		t.Errorf("api task: roots nil=%v interval=%v", api.Roots == nil, api.Interval(ctx))
	}
	// A collector that has no background mode gets no task.
	if _, ok := b.taskFor(&bgCollector{name: model.SourceManual}); ok {
		t.Error("manual source got a task")
	}
}

func TestBackgroundStartStop(t *testing.T) {
	st := openStore(t)
	svc := newBgService(t, st, fixedNow, &bgCollector{name: model.SourceClaudeCode})
	b := NewBackground(svc, fastBg())
	b.Stop() // without Start: no-op
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := b.Start(context.Background()); err == nil {
		t.Fatal("second Start accepted")
	}
	b.Stop()
	b.Stop()
}

// ---- partial scans of the service ------------------------------------------

func TestRunScanSourcesKeepsOtherSteps(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cc := &bgCollector{name: model.SourceClaudeCode}
	codex := &bgCollector{name: model.SourceCodexCLI, run: func(context.Context) (collector.Result, error) {
		return collector.Result{Warnings: []string{"odd line"}}, nil
	}}
	svc := newBgService(t, st, fixedNow, cc, codex)

	if _, err := svc.Rescan(ctx, nil); err != nil { // everything ran once
		t.Fatal(err)
	}
	id, ok := svc.BeginScan()
	if !ok {
		t.Fatal("busy")
	}
	sum := svc.RunScanSources(ctx, id, []model.Source{model.SourceClaudeCode}, nil)

	if cc.calls.Load() != 2 || codex.calls.Load() != 1 {
		t.Fatalf("calls cc=%d codex=%d, want 2 and 1", cc.calls.Load(), codex.calls.Load())
	}
	if len(sum.Steps) != 2 {
		t.Fatalf("steps = %+v, want both sources", sum.Steps)
	}
	if sum.Steps[0].Source != "claude_code" || sum.Steps[0].ScanID != id {
		t.Errorf("fresh step = %+v", sum.Steps[0])
	}
	if sum.Steps[1].Source != "codex_cli" || sum.Steps[1].ScanID == id || len(sum.Steps[1].Warnings) != 1 {
		t.Errorf("carried step = %+v", sum.Steps[1])
	}
	_, _, last := svc.scanState()
	if last == nil || last.ID != id || len(last.Steps) != 2 {
		t.Errorf("last = %+v", last)
	}
}

func TestScanObserver(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow, &bgCollector{name: model.SourceClaudeCode})

	var seen []int64
	svc.SetScanObserver(func(s ScanSummaryDTO) {
		// The service already reports the scan as finished.
		if svc.Scanning() {
			t.Error("observer ran while the scan was still marked running")
		}
		seen = append(seen, s.ID)
	})
	if _, err := svc.Rescan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	svc.SetScanObserver(nil)
	if _, err := svc.Rescan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 1 || seen[0] != 1 {
		t.Fatalf("observer saw %v, want [1]", seen)
	}

	// A canceled scan is not reported.
	svc.SetScanObserver(func(ScanSummaryDTO) { t.Error("observer called for a canceled scan") })
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := svc.Rescan(canceled, nil); err != nil {
		t.Fatal(err)
	}
}

func TestMergeStepsAndFilter(t *testing.T) {
	a, b, c := &bgCollector{name: model.SourceClaudeCode}, &bgCollector{name: model.SourceCodexCLI}, &bgCollector{name: model.SourceGeminiCLI}
	all := []collector.Collector{a, b, c}

	got := filterCollectors(all, []model.Source{model.SourceGeminiCLI, model.SourceClaudeCode})
	if len(got) != 2 || got[0] != collector.Collector(a) || got[1] != collector.Collector(c) {
		t.Fatalf("filter kept %v, want registry order [claude_code gemini_cli]", got)
	}

	prev := &ScanSummaryDTO{Steps: []ScanStep{{Source: "codex_cli", Status: StatusError, Error: "old"}, {Source: "claude_code", Status: StatusDone}}}
	steps := mergeSteps(prev, []ScanStep{{Source: "gemini_cli", Status: StatusDone}}, all)
	var order []string
	for _, s := range steps {
		order = append(order, s.Source+":"+s.Status)
	}
	if strings.Join(order, ",") != "claude_code:done,codex_cli:error,gemini_cli:done" {
		t.Fatalf("merged = %v", order)
	}
	if got := mergeSteps(nil, nil, all); len(got) != 0 {
		t.Fatalf("merge of nothing = %v", got)
	}
}

// ---- budget notifications ---------------------------------------------------

func TestBudgetBreachNotifiesOncePerPeriod(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := testNow
	svc := newBgService(t, st, func() time.Time { return now })
	n := &fakeNotifier{}
	b := NewBackground(svc, BackgroundConfig{Notifier: n})

	tokenBudget(t, st, store.BudgetPeriodDaily, 1_000)
	insert(t, st, rec("a", now.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 600, 600, 0))

	sent, err := b.evaluateBudgets(ctx)
	if err != nil || sent != 1 {
		t.Fatalf("first evaluation: sent=%d err=%v", sent, err)
	}
	// More scans in the same period must not repeat the notification.
	for i := 0; i < 3; i++ {
		insert(t, st, rec("more"+string(rune('a'+i)), now.Add(-time.Minute), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 100, 100, 0))
		if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
			t.Fatalf("repeat evaluation %d: sent=%d err=%v", i, sent, err)
		}
	}
	msgs := n.messages()
	if len(msgs) != 1 {
		t.Fatalf("notifications = %d, want 1: %+v", len(msgs), msgs)
	}
	if !strings.Contains(msgs[0].Title, "Bütçe") || !strings.Contains(msgs[0].Body, "Günlük") ||
		!strings.Contains(msgs[0].Body, "1,2 bin / 1 bin token") {
		t.Errorf("message = %+v", msgs[0])
	}
	if !strings.HasPrefix(msgs[0].ID, "budget-") {
		t.Errorf("id = %q", msgs[0].ID)
	}

	// The next day is a new period: the breach (usage keeps counting from
	// zero) is notified again once it is reached again.
	now = now.Add(24 * time.Hour)
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
		t.Fatalf("new day without usage: sent=%d err=%v", sent, err)
	}
	insert(t, st, rec("next", now.Add(-time.Minute), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 700, 700, 0))
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 1 {
		t.Fatalf("new period: sent=%d err=%v", sent, err)
	}
	if got := len(n.messages()); got != 2 {
		t.Fatalf("notifications = %d, want 2", got)
	}
}

func TestBudgetNotificationRetriedAfterFailure(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	n := &fakeNotifier{fail: errors.New("no notification service")}
	b := NewBackground(svc, BackgroundConfig{Notifier: n})

	tokenBudget(t, st, store.BudgetPeriodDaily, 100)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 300, 300, 0))

	sent, err := b.evaluateBudgets(ctx)
	if err == nil || sent != 0 {
		t.Fatalf("failing notifier: sent=%d err=%v", sent, err)
	}
	// Not marked: the next scan tries again, and once delivered it is final.
	n.setFail(nil)
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 1 {
		t.Fatalf("retry: sent=%d err=%v", sent, err)
	}
	if sent, _ := b.evaluateBudgets(ctx); sent != 0 {
		t.Fatalf("after delivery: sent=%d", sent)
	}
}

func TestBudgetNotificationsCanBeSwitchedOff(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	n := &fakeNotifier{}
	b := NewBackground(svc, BackgroundConfig{Notifier: n})
	tokenBudget(t, st, store.BudgetPeriodDaily, 100)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 300, 300, 0))

	if err := st.SetSetting(ctx, SettingBudgetNotifications, false); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
		t.Fatalf("switched off: sent=%d err=%v", sent, err)
	}
	if err := st.SetSetting(ctx, SettingBudgetNotifications, true); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 1 {
		t.Fatalf("switched on: sent=%d err=%v", sent, err)
	}
}

func TestBudgetNoNotifier(t *testing.T) {
	st := openStore(t)
	b := NewBackground(newBgService(t, st, fixedNow), BackgroundConfig{})
	tokenBudget(t, st, store.BudgetPeriodDaily, 1)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 300, 300, 0))
	if sent, err := b.evaluateBudgets(context.Background()); err != nil || sent != 0 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	// Nothing was marked: a notifier that appears later still gets it.
	n := &fakeNotifier{}
	b.cfg.Notifier = n
	if sent, err := b.evaluateBudgets(context.Background()); err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
}

// A scan, whoever starts it, ends in a budget check.
func TestBackgroundChecksBudgetsAfterEveryScan(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cc := &bgCollector{name: model.SourceClaudeCode, run: func(context.Context) (collector.Result, error) {
		insert(t, st, rec("scan-added", testNow.Add(-time.Minute), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 500, 500, 0))
		return collector.Result{Added: 1, Files: 1}, nil
	}}
	svc := newBgService(t, st, fixedNow, cc)
	n := &fakeNotifier{}
	tokenBudget(t, st, store.BudgetPeriodDaily, 800)
	startBg(t, svc, BackgroundConfig{Scheduler: fastBg().Scheduler, Notifier: n})

	// The initial check finds nothing: no usage yet.
	stays(t, "a notification before any usage", 100*time.Millisecond, func() bool { return len(n.messages()) > 0 })

	// A manual scan (not started by the scheduler) pushes usage over the
	// budget; the observer triggers the check.
	if _, err := svc.Rescan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the budget notification", func() bool { return len(n.messages()) == 1 })
	if _, err := svc.Rescan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	stays(t, "a second notification", 200*time.Millisecond, func() bool { return len(n.messages()) > 1 })
}

func TestBreachMessage(t *testing.T) {
	tokens, usd := int64(2_500_000), 10.0
	m := breachMessage(budget.Breach{
		BudgetID: 7, Scope: store.BudgetScopeService, ScopeValue: "anthropic",
		Period: store.BudgetPeriodMonthly, PeriodKey: "2026-11",
		ThresholdTokens: &tokens, ThresholdUSD: &usd,
		UsedTokens: 3_100_000, UsedUSD: 12.345, Estimated: true,
		TokensExceeded: true, USDExceeded: true,
	})
	if m.ID != "budget-7-2026-11" {
		t.Errorf("id = %q", m.ID)
	}
	want := "Aylık bütçe (anthropic): 3,1 milyon / 2,5 milyon token, $12,35 / $10,00 (tahmini)"
	if m.Body != want {
		t.Errorf("body = %q, want %q", m.Body, want)
	}

	// Only the exceeded threshold is mentioned.
	m = breachMessage(budget.Breach{
		Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily,
		ThresholdTokens: &tokens, ThresholdUSD: &usd,
		UsedTokens: 999, UsedUSD: 11, USDExceeded: true,
	})
	if m.Body != "Günlük bütçe (toplam): $11,00 / $10,00" {
		t.Errorf("body = %q", m.Body)
	}

	for n, want := range map[int64]string{0: "0", 999: "999", 1000: "1 bin", 12_500: "12,5 bin", 1_000_000: "1 milyon", 2_100_000_000: "2,1 milyar"} {
		if got := formatTokens(n); got != want {
			t.Errorf("formatTokens(%d) = %q, want %q", n, got, want)
		}
	}
}

// ---- review fixes ---------------------------------------------------------

func TestBackgroundStopGivesUpOnStuckWork(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root := t.TempDir()
	if err := st.SetSetting(ctx, "sources.claude_code.path", root); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	entered := make(chan struct{}, 1)
	cc := &bgCollector{name: model.SourceClaudeCode, run: func(context.Context) (collector.Result, error) {
		entered <- struct{}{}
		<-release // ignores cancellation, like a blocked dialog or HTTP call
		return collector.Result{}, nil
	}}
	svc := newBgService(t, st, fixedNow, cc)
	cfg := fastBg()
	cfg.StopTimeout = 100 * time.Millisecond
	b := NewBackground(svc, cfg)
	if err := b.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "x.jsonl"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("collector never started")
	}
	start := time.Now()
	b.Stop()
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("Stop took %v with a 100ms timeout", d)
	}
	close(release) // let the goroutines end before the store closes
	waitUntil(t, "the stuck scan to finish", func() bool { return !svc.Scanning() })
}

func TestPartialScanIsInvisibleAndSilentWhenNothingChanged(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	root := t.TempDir()
	if err := st.SetSetting(ctx, "sources.claude_code.path", root); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	result := collector.Result{} // nothing new
	inScan := make(chan struct{})
	hold := make(chan struct{})
	cc := &bgCollector{name: model.SourceClaudeCode, run: func(context.Context) (collector.Result, error) {
		mu.Lock()
		r := result
		mu.Unlock()
		select {
		case inScan <- struct{}{}:
			<-hold
		default:
		}
		return r, nil
	}}
	svc := newBgService(t, st, fixedNow, cc)
	var done atomic.Int32
	startBg(t, svc, BackgroundConfig{Scheduler: fastBg().Scheduler, OnDone: func(ScanSummaryDTO) { done.Add(1) }})

	trigger := func() {
		if err := os.WriteFile(filepath.Join(root, "x.jsonl"), []byte(time.Now().String()), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	trigger()
	<-inScan // the background scan is running now
	if !svc.PartialScanRunning() || !svc.Scanning() {
		t.Fatal("expected a running partial scan")
	}
	scanning, id, _ := svc.scanState()
	if scanning || id != 0 {
		t.Fatalf("a background scan is reported as running: scanning=%v id=%d", scanning, id)
	}
	close(hold)
	waitUntil(t, "the scan to end", func() bool { return !svc.Scanning() })
	stays(t, "a done event for a scan that found nothing", 200*time.Millisecond, func() bool { return done.Load() > 0 })

	// New data is reported.
	mu.Lock()
	result = collector.Result{Added: 2}
	mu.Unlock()
	trigger()
	waitUntil(t, "the done event for new data", func() bool { return done.Load() == 1 })

	// A problem is reported, and so is its end.
	mu.Lock()
	result = collector.Result{Warnings: []string{"odd"}}
	mu.Unlock()
	trigger()
	waitUntil(t, "the done event for a warning", func() bool { return done.Load() == 2 })
	mu.Lock()
	result = collector.Result{}
	mu.Unlock()
	trigger()
	waitUntil(t, "the done event that clears the warning", func() bool { return done.Load() == 3 })
	trigger()
	stays(t, "an event for a quiet scan", 250*time.Millisecond, func() bool { return done.Load() > 3 })
}

func TestPartialScanRunningFlag(t *testing.T) {
	st := openStore(t)
	svc := newBgService(t, st, fixedNow, &bgCollector{name: model.SourceClaudeCode})
	if svc.PartialScanRunning() {
		t.Fatal("partial before any scan")
	}
	id, ok := svc.beginScan(true)
	if !ok || !svc.PartialScanRunning() {
		t.Fatal("partial scan not flagged")
	}
	svc.RunScanSources(context.Background(), id, []model.Source{model.SourceClaudeCode}, nil)
	if svc.PartialScanRunning() {
		t.Fatal("flag survived the scan")
	}
	id, _ = svc.BeginScan()
	if svc.PartialScanRunning() {
		t.Fatal("full scan flagged as partial")
	}
	svc.RunScan(context.Background(), id, nil)
}

// ---- queued full scan -------------------------------------------------------

func TestQueuedFullScanWaitsForBackgroundRefreshAndCollapses(t *testing.T) {
	old := queuePoll
	queuePoll = 5 * time.Millisecond
	t.Cleanup(func() { queuePoll = old })

	ctx := context.Background()
	st := openStore(t)
	cc := &bgCollector{name: model.SourceClaudeCode}
	codex := &bgCollector{name: model.SourceCodexCLI}
	svc := newBgService(t, st, fixedNow, cc, codex)

	// A background refresh of one source holds the slot.
	partialID, ok := svc.beginScan(true)
	if !ok {
		t.Fatal("slot busy")
	}

	var ran atomic.Int32
	var wg sync.WaitGroup
	results := make([]bool, 5)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = svc.RunQueuedFullScan(ctx, func(id int64) {
				ran.Add(1)
				svc.RunScan(ctx, id, nil)
			})
		}()
	}
	// Four of the five requests find a waiter and return at once.
	waitUntil(t, "the extra requests to collapse", func() bool {
		svc.mu.Lock()
		defer svc.mu.Unlock()
		return svc.fullQueued
	})
	stays(t, "the full scan before the slot is free", 100*time.Millisecond, func() bool { return ran.Load() > 0 })

	svc.RunScanSources(ctx, partialID, []model.Source{model.SourceClaudeCode}, nil)
	wg.Wait()

	if ran.Load() != 1 {
		t.Fatalf("full scans = %d, want 1", ran.Load())
	}
	n := 0
	for _, r := range results {
		if r {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("%d requests ran, want 1: %v", n, results)
	}
	// The full scan covered every source: claude_code ran in the partial scan
	// and again in the full one, codex_cli only in the full one.
	if cc.calls.Load() != 2 || codex.calls.Load() != 1 {
		t.Fatalf("calls cc=%d codex=%d, want 2 and 1", cc.calls.Load(), codex.calls.Load())
	}
	// The queue is free again.
	pid, _ := svc.beginScan(true)
	done := make(chan bool)
	go func() { done <- svc.RunQueuedFullScan(ctx, func(id int64) { svc.RunScan(ctx, id, nil) }) }()
	time.Sleep(30 * time.Millisecond)
	svc.RunScanSources(ctx, pid, []model.Source{model.SourceClaudeCode}, nil)
	if !<-done {
		t.Fatal("a later request did not run")
	}
}

func TestQueuedFullScanLeavesOnCancel(t *testing.T) {
	old := queuePoll
	queuePoll = 5 * time.Millisecond
	t.Cleanup(func() { queuePoll = old })

	st := openStore(t)
	svc := newBgService(t, st, fixedNow, &bgCollector{name: model.SourceClaudeCode})
	pid, _ := svc.beginScan(true)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan bool)
	go func() { done <- svc.RunQueuedFullScan(ctx, func(int64) { t.Error("ran after cancel") }) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case ran := <-done:
		if ran {
			t.Fatal("reported a run")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the waiter did not leave")
	}
	svc.mu.Lock()
	queued := svc.fullQueued
	svc.mu.Unlock()
	if queued {
		t.Fatal("queue flag stuck after cancel")
	}
	svc.RunScanSources(context.Background(), pid, []model.Source{model.SourceClaudeCode}, nil)
}

// ---- budget alert (tray) ----------------------------------------------------

// alertRecorder collects the values OnBudgetAlert was called with.
type alertRecorder struct {
	mu  sync.Mutex
	got []bool
}

func (r *alertRecorder) record(alert bool) {
	r.mu.Lock()
	r.got = append(r.got, alert)
	r.mu.Unlock()
}

func (r *alertRecorder) last(t *testing.T) bool {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.got) == 0 {
		t.Fatal("OnBudgetAlert was not called")
	}
	return r.got[len(r.got)-1]
}

func TestBudgetAlertFollowsTheBreachState(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	n := &fakeNotifier{}
	alerts := &alertRecorder{}
	b := NewBackground(svc, BackgroundConfig{Notifier: n, OnBudgetAlert: alerts.record})

	// No budget: no alert.
	if _, err := b.evaluateBudgets(ctx); err != nil {
		t.Fatal(err)
	}
	if alerts.last(t) {
		t.Fatal("alert without a budget")
	}

	// A budget under its threshold: no alert.
	id := tokenBudget(t, st, store.BudgetPeriodDaily, 1_000)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 100, 100, 0))
	if _, err := b.evaluateBudgets(ctx); err != nil {
		t.Fatal(err)
	}
	if alerts.last(t) {
		t.Fatal("alert under the threshold")
	}

	// Over the threshold: alert, and it stays on after the notification was
	// delivered (the breach is no longer "new" but the budget is still over).
	insert(t, st, rec("b", testNow.Add(-time.Minute), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 600, 600, 0))
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if !alerts.last(t) {
		t.Fatal("no alert over the threshold")
	}
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
		t.Fatalf("repeat: sent=%d err=%v", sent, err)
	}
	if !alerts.last(t) {
		t.Fatal("alert dropped after the notification was delivered")
	}

	// The budget is removed: back to normal.
	if err := st.DeleteBudget(ctx, id); err != nil {
		t.Fatal(err)
	}
	if _, err := b.evaluateBudgets(ctx); err != nil {
		t.Fatal(err)
	}
	if alerts.last(t) {
		t.Fatal("alert after the budget was deleted")
	}
}

func TestBudgetAlertIgnoresTheNotificationSetting(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	n := &fakeNotifier{}
	alerts := &alertRecorder{}
	b := NewBackground(svc, BackgroundConfig{Notifier: n, OnBudgetAlert: alerts.record})
	tokenBudget(t, st, store.BudgetPeriodDaily, 100)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 300, 300, 0))
	if err := st.SetSetting(ctx, SettingBudgetNotifications, false); err != nil {
		t.Fatal(err)
	}

	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
		t.Fatalf("switched off: sent=%d err=%v", sent, err)
	}
	if !alerts.last(t) {
		t.Fatal("no alert while the notifications are off")
	}
	if got := len(n.messages()); got != 0 {
		t.Fatalf("notifications = %d while switched off", got)
	}

	// Switching the notifications on delivers the pending one; the alert
	// stays.
	if err := st.SetSetting(ctx, SettingBudgetNotifications, true); err != nil {
		t.Fatal(err)
	}
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 1 {
		t.Fatalf("switched on: sent=%d err=%v", sent, err)
	}
	if !alerts.last(t) {
		t.Fatal("alert lost after the notification")
	}
}

func TestBudgetAlertWithoutNotifierAndOnFailedDelivery(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	tokenBudget(t, st, store.BudgetPeriodDaily, 100)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 300, 300, 0))

	// No notifier at all.
	r1 := &alertRecorder{}
	b := NewBackground(newBgService(t, st, fixedNow), BackgroundConfig{OnBudgetAlert: r1.record})
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if !r1.last(t) {
		t.Fatal("no alert without a notifier")
	}

	// A notifier that fails: the budget is over its threshold anyway.
	r2 := &alertRecorder{}
	n := &fakeNotifier{fail: errors.New("no notification service")}
	b = NewBackground(newBgService(t, st, fixedNow), BackgroundConfig{Notifier: n, OnBudgetAlert: r2.record})
	if _, err := b.evaluateBudgets(ctx); err == nil {
		t.Fatal("want the delivery error")
	}
	if !r2.last(t) {
		t.Fatal("no alert when the delivery failed")
	}
}

// After a scan pushes usage over a budget, the callback reports it; a new
// period (the day ends) clears it again.
func TestBackgroundReportsBudgetAlertAfterScans(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	now := testNow
	var nowMu sync.Mutex
	clock := func() time.Time { nowMu.Lock(); defer nowMu.Unlock(); return now }
	cc := &bgCollector{name: model.SourceClaudeCode, run: func(context.Context) (collector.Result, error) {
		insert(t, st, rec("scan-added", testNow.Add(-time.Minute), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 500, 500, 0))
		return collector.Result{Added: 1, Files: 1}, nil
	}}
	svc := newBgService(t, st, clock, cc)
	tokenBudget(t, st, store.BudgetPeriodDaily, 800)
	r := &alertRecorder{}
	startBg(t, svc, BackgroundConfig{Scheduler: fastBg().Scheduler, OnBudgetAlert: r.record})

	// The initial check (after Start) reports "no alert".
	waitUntil(t, "the initial check", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return len(r.got) > 0 })
	if r.last(t) {
		t.Fatal("alert before any usage")
	}

	if _, err := svc.Rescan(ctx, nil); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the alert", func() bool { r.mu.Lock(); defer r.mu.Unlock(); return r.got[len(r.got)-1] })

	// Next day: a new period without usage; the next check clears it.
	nowMu.Lock()
	now = now.Add(24 * time.Hour)
	nowMu.Unlock()
	b := NewBackground(svc, BackgroundConfig{OnBudgetAlert: r.record})
	if _, err := b.evaluateBudgets(ctx); err != nil {
		t.Fatal(err)
	}
	if r.last(t) {
		t.Fatal("alert still on in a new period")
	}
}

// A budget that was notified is measured anew: when its usage falls below the
// threshold again (a manual record is deleted) the alert is gone, although the
// notification record stays.
func TestBudgetAlertClearsWhenNotifiedUsageFalls(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	n := &fakeNotifier{}
	alerts := &alertRecorder{}
	b := NewBackground(svc, BackgroundConfig{Notifier: n, OnBudgetAlert: alerts.record})
	tokenBudget(t, st, store.BudgetPeriodMonthly, 1_000)
	insert(t, st, rec("small", testNow.Add(-2*time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 100, 100, 0))
	insert(t, st, rec("big", testNow.Add(-time.Hour), model.SourceManual, "anthropic", "claude-sonnet-4-5", "p", "s", 900, 900, 0))

	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 1 {
		t.Fatalf("sent=%d err=%v", sent, err)
	}
	if !alerts.last(t) {
		t.Fatal("no alert over the threshold")
	}
	if _, err := b.evaluateBudgets(ctx); err != nil || !alerts.last(t) {
		t.Fatalf("alert after delivery: err=%v", err)
	}

	if found, err := st.DeleteManualRecord(ctx, "big"); err != nil || !found {
		t.Fatalf("delete: found=%v err=%v", found, err)
	}
	if sent, err := b.evaluateBudgets(ctx); err != nil || sent != 0 {
		t.Fatalf("after delete: sent=%d err=%v", sent, err)
	}
	if alerts.last(t) {
		t.Fatal("alert stays on although the usage fell below the threshold")
	}
	if got := len(n.messages()); got != 1 {
		t.Fatalf("notifications = %d, want 1 (no repeat)", got)
	}
}

// Adding, editing and deleting a budget re-evaluates at once, without a scan.
func TestBudgetChangesReevaluateTheAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-4-5", "p", "s", 300, 300, 0))
	alerts := &alertRecorder{}
	startBg(t, svc, BackgroundConfig{Scheduler: fastBg().Scheduler, OnBudgetAlert: alerts.record})
	current := func() bool {
		alerts.mu.Lock()
		defer alerts.mu.Unlock()
		return len(alerts.got) > 0 && alerts.got[len(alerts.got)-1]
	}
	called := func() bool { alerts.mu.Lock(); defer alerts.mu.Unlock(); return len(alerts.got) > 0 }

	waitUntil(t, "the initial check", called)
	if current() {
		t.Fatal("alert without a budget")
	}

	low := int64(100)
	saved, err := svc.SaveBudget(ctx, BudgetDTO{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: &low, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the alert after adding a budget", current)

	high := int64(1_000_000)
	saved.ThresholdTokens = &high
	if _, err := svc.SaveBudget(ctx, saved); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "no alert after raising the threshold", func() bool { return !current() })

	saved.ThresholdTokens = &low
	if _, err := svc.SaveBudget(ctx, saved); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the alert after lowering the threshold", current)

	if err := svc.DeleteBudget(ctx, saved.ID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "no alert after deleting the budget", func() bool { return !current() })
}

// Whatever changes usage or prices re-evaluates the alert at once: manual
// records (add, edit, delete), imports and price overrides.
func TestUsageAndPriceChangesReevaluateTheAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	alerts := &alertRecorder{}
	startBg(t, svc, BackgroundConfig{Scheduler: fastBg().Scheduler, OnBudgetAlert: alerts.record})
	current := func() bool {
		alerts.mu.Lock()
		defer alerts.mu.Unlock()
		return len(alerts.got) > 0 && alerts.got[len(alerts.got)-1]
	}
	waitUntil(t, "the initial check", func() bool { alerts.mu.Lock(); defer alerts.mu.Unlock(); return len(alerts.got) > 0 })

	tokenBudget(t, st, store.BudgetPeriodDaily, 1_000)
	waitUntil(t, "settled", func() bool { return !current() })

	manual := ManualRecordDTO{
		TimestampMs: testNow.Add(-time.Hour).UnixMilli(), Service: "anthropic", Model: "claude-sonnet-4-5",
		InputTokens: 10, OutputTokens: 10, RequestCount: 1,
	}
	added, err := svc.AddManualRecord(ctx, manual)
	if err != nil {
		t.Fatal(err)
	}
	if current() {
		t.Fatal("alert under the threshold")
	}

	added.InputTokens, added.OutputTokens = 900, 900
	if _, err := svc.UpdateManualRecord(ctx, added); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the alert after editing a record", current)

	if err := svc.DeleteManualRecord(ctx, added.ExternalID); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "no alert after deleting the record", func() bool { return !current() })

	// An import counts too.
	csv := filepath.Join(t.TempDir(), "usage.csv")
	row := "timestamp,service,model,input_tokens,output_tokens\n" +
		testNow.Add(-time.Hour).UTC().Format(time.RFC3339) + ",anthropic,claude-sonnet-4-5,900,900\n"
	if err := os.WriteFile(csv, []byte(row), 0o600); err != nil {
		t.Fatal(err)
	}
	if res, err := svc.ImportCSVFile(ctx, csv); err != nil || res.Added != 1 {
		t.Fatalf("import: %+v, %v", res, err)
	}
	waitUntil(t, "the alert after an import", current)
}

func TestPriceOverrideChangesReevaluateTheAlert(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	svc := newBgService(t, st, fixedNow)
	alerts := &alertRecorder{}
	startBg(t, svc, BackgroundConfig{Scheduler: fastBg().Scheduler, OnBudgetAlert: alerts.record})
	current := func() bool {
		alerts.mu.Lock()
		defer alerts.mu.Unlock()
		return len(alerts.got) > 0 && alerts.got[len(alerts.got)-1]
	}
	waitUntil(t, "the initial check", func() bool { alerts.mu.Lock(); defer alerts.mu.Unlock(); return len(alerts.got) > 0 })

	// A USD budget on a model without a price: the cost is unknown, no alert.
	insert(t, st, rec("a", testNow.Add(-time.Hour), model.SourceClaudeCode, "acme", "acme-model-1", "p", "s", 1_000_000, 0, 0))
	usd := 5.0
	if _, err := svc.SaveBudget(ctx, BudgetDTO{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: &usd, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	stays(t, "an alert for a model without a price", 150*time.Millisecond, current)

	price := 10.0 // USD per million input tokens: 1M tokens cost 10 USD
	if err := svc.SetPriceOverride(ctx, PriceOverrideDTO{Model: "acme-model-1", Input: &price, Output: &price}); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "the alert after setting a price", current)

	if err := svc.ResetPriceOverride(ctx, "acme-model-1"); err != nil {
		t.Fatal(err)
	}
	waitUntil(t, "no alert after resetting the price", func() bool { return !current() })
}
