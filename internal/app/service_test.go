package app

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/limits"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/pricing"
	"github.com/muhammetsafak/TokenMetrics/internal/secrets"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

var testLoc = time.FixedZone("TST", 3*3600)

// testNow is 2026-11-06 15:30 local (12:30 UTC).
var testNow = time.Date(2026, 11, 6, 15, 30, 0, 0, testLoc)

type fakeCollector struct {
	name  model.Source
	run   func(ctx context.Context) (collector.Result, error)
	calls int
}

func (f *fakeCollector) Name() model.Source { return f.name }
func (f *fakeCollector) Collect(ctx context.Context, _ collector.Deps) (collector.Result, error) {
	f.calls++
	if f.run != nil {
		return f.run(ctx)
	}
	return collector.Result{Added: 1, Files: 1}, nil
}

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func newService(t *testing.T, st *store.Store, cols ...collector.Collector) *Service {
	t.Helper()
	reg := collector.NewRegistry()
	for _, c := range cols {
		if err := reg.Register(c); err != nil {
			t.Fatal(err)
		}
	}
	s, err := New(Config{
		Store: st, DeviceID: "dev", Registry: reg,
		Now:      func() time.Time { return testNow },
		Location: testLoc,
		HasKey:   func(string) (bool, error) { return false, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func rec(id string, ts time.Time, src model.Source, service, mdl, project, session string, in, out, cacheRead int64) model.UsageRecord {
	return model.UsageRecord{
		Source: src, ExternalID: id, DeviceID: "dev", Timestamp: ts,
		Service: service, Model: mdl, Project: project, Session: session,
		InputTokens: in, OutputTokens: out, CacheReadTokens: cacheRead, RequestCount: 1,
	}
}

func insert(t *testing.T, st *store.Store, recs ...model.UsageRecord) {
	t.Helper()
	if _, _, err := st.InsertBatch(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
}

func day(n int, h int) time.Time { // local day offset from today, at hour h
	return time.Date(2026, 11, 6+n, h, 0, 0, 0, testLoc)
}

func TestRescan(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	cc := &fakeCollector{name: model.SourceClaudeCode}
	codex := &fakeCollector{name: model.SourceCodexCLI, run: func(context.Context) (collector.Result, error) {
		return collector.Result{}, errors.New("boom")
	}}
	gem := &fakeCollector{name: model.SourceGeminiCLI, run: func(context.Context) (collector.Result, error) {
		panic("parser bug")
	}}
	api := &fakeCollector{name: model.SourceAnthropicAPI}
	off := &fakeCollector{name: model.SourceOpenAIAPI}
	s := newService(t, st, cc, codex, gem, api, off)
	s.hasKey = func(p string) (bool, error) { return p == secrets.ProviderOpenAI, nil }
	if err := st.SetSetting(ctx, "sources.openai_api.enabled", false); err != nil {
		t.Fatal(err)
	}

	var steps []ScanStep
	sum, err := s.Rescan(ctx, func(st ScanStep) { steps = append(steps, st) })
	if err != nil {
		t.Fatal(err)
	}
	status := map[string]string{}
	for _, st := range sum.Steps {
		status[st.Source] = st.Status
	}
	want := map[string]string{
		"claude_code":   StatusDone,
		"codex_cli":     StatusError,
		"gemini_cli":    StatusError,
		"anthropic_api": StatusSkipped, // no key
		"openai_api":    StatusSkipped, // key present but switched off
	}
	for k, v := range want {
		if status[k] != v {
			t.Errorf("status[%s] = %q, want %q", k, status[k], v)
		}
	}
	if api.calls != 0 || off.calls != 0 {
		t.Errorf("skipped collectors ran: api=%d off=%d", api.calls, off.calls)
	}
	if sum.Added != 1 {
		t.Errorf("Added = %d, want 1", sum.Added)
	}
	// running + terminal event for each collector that ran, one for skipped.
	if len(steps) != 3*2+2 {
		t.Errorf("got %d step events, want 8: %+v", len(steps), steps)
	}
	meta, err := s.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if meta.LastScanMs != testNow.UnixMilli() {
		t.Errorf("LastScanMs = %d, want %d", meta.LastScanMs, testNow.UnixMilli())
	}
}

func TestRescanSingleFlightAndCancel(t *testing.T) {
	st := openStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	slow := &fakeCollector{name: model.SourceClaudeCode, run: func(ctx context.Context) (collector.Result, error) {
		close(started)
		select {
		case <-release:
		case <-ctx.Done():
			return collector.Result{}, ctx.Err()
		}
		return collector.Result{}, nil
	}}
	s := newService(t, st, slow)

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	var first ScanSummaryDTO
	wg.Add(1)
	go func() {
		defer wg.Done()
		first, _ = s.Rescan(ctx, nil)
	}()
	<-started
	if !s.Scanning() {
		t.Error("Scanning() = false during a scan")
	}
	if _, err := s.Rescan(context.Background(), nil); !errors.Is(err, ErrScanRunning) {
		t.Errorf("second Rescan error = %v, want ErrScanRunning", err)
	}
	cancel()
	wg.Wait()
	if !first.Canceled {
		t.Error("canceled scan not marked Canceled")
	}
	if s.Scanning() {
		t.Error("Scanning() = true after the scan ended")
	}
	var last int64
	if found, _ := st.GetSetting(context.Background(), SettingLastScan, &last); found {
		t.Error("a canceled scan must not record a last-scan time")
	}
}

func TestSummary(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	const mdl = "claude-sonnet-5-5"
	insert(t, st,
		rec("today", day(0, 9), model.SourceClaudeCode, "anthropic", mdl, "/p", "s1", 1000, 500, 2000),
		rec("yday", day(-1, 23), model.SourceClaudeCode, "anthropic", mdl, "/p", "s1", 100, 0, 0),
		rec("d6", day(-6, 0), model.SourceClaudeCode, "anthropic", mdl, "/p", "s2", 10, 0, 0),     // inside 7d (first instant)
		rec("d7", day(-7, 23), model.SourceClaudeCode, "anthropic", mdl, "/p", "s2", 1, 0, 0),     // outside 7d
		rec("d29", day(-29, 0), model.SourceClaudeCode, "anthropic", mdl, "/p", "s2", 5, 0, 0),    // inside 30d
		rec("d30", day(-30, 23), model.SourceClaudeCode, "anthropic", mdl, "/p", "s2", 7, 0, 0),   // outside 30d
		rec("future", day(1, 1), model.SourceClaudeCode, "anthropic", mdl, "/p", "s3", 999, 0, 0), // tomorrow: in no card
	)
	got, err := s.Summary(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if got.Today.TotalTokens != 3500 || got.Today.Records != 1 {
		t.Errorf("today = %+v", got.Today)
	}
	if got.Last7d.TotalTokens != 3500+100+10 {
		t.Errorf("7d total = %d, want 3610", got.Last7d.TotalTokens)
	}
	if got.Last30d.TotalTokens != 3500+100+10+1+5 {
		t.Errorf("30d total = %d, want 3616", got.Last30d.TotalTokens)
	}
	if got.Selected.Records != 7 {
		t.Errorf("selected records = %d, want 7 (no time bound)", got.Selected.Records)
	}
	if got.PriceAsOf == "" || got.TimeZone != "TST" {
		t.Errorf("meta fields: asOf=%q tz=%q", got.PriceAsOf, got.TimeZone)
	}
	if got.DayStartMs != day(0, 0).UnixMilli() {
		t.Errorf("DayStartMs = %d", got.DayStartMs)
	}

	// Cost matches the pricer and is flagged as an estimate.
	p, err := pricing.Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	wantUSD, _, known := p.Cost(rec("x", day(0, 9), model.SourceClaudeCode, "anthropic", mdl, "", "", 1000, 500, 2000))
	if !known {
		t.Fatal("test model has no price")
	}
	if got.Today.CostUSD == nil || diff(*got.Today.CostUSD, wantUSD) > 1e-9 {
		t.Errorf("today cost = %v, want %v", got.Today.CostUSD, wantUSD)
	}
	if !got.Today.CostEstimated {
		t.Error("cost from the price table must be flagged estimated")
	}

	// The selected range and the other filters apply; the cards ignore only the range.
	sel, err := s.Summary(ctx, FilterDTO{FromMs: day(-1, 0).UnixMilli(), ToMs: day(0, 0).UnixMilli(), Sources: []string{"claude_code"}})
	if err != nil {
		t.Fatal(err)
	}
	if sel.Selected.TotalTokens != 100 {
		t.Errorf("selected = %d, want 100", sel.Selected.TotalTokens)
	}
	other, err := s.Summary(ctx, FilterDTO{Sources: []string{"codex_cli"}})
	if err != nil {
		t.Fatal(err)
	}
	if other.Today.Records != 0 || other.Last30d.Records != 0 {
		t.Errorf("source filter ignored by cards: %+v", other.Last30d)
	}
}

func diff(a, b float64) float64 {
	if a > b {
		return a - b
	}
	return b - a
}

func TestFilterValidation(t *testing.T) {
	s := newService(t, openStore(t))
	ctx := context.Background()
	if _, err := s.Summary(ctx, FilterDTO{Sources: []string{"nope"}}); err == nil {
		t.Error("unknown source accepted")
	}
	if _, err := s.Summary(ctx, FilterDTO{FromMs: 10, ToMs: 5}); err == nil {
		t.Error("empty range accepted")
	}
	if _, err := s.TimeSeries(ctx, FilterDTO{}, "year", ""); err == nil {
		t.Error("unknown bucket accepted")
	}
	if _, err := s.TimeSeries(ctx, FilterDTO{}, "day", "project"); err == nil {
		t.Error("stack by project accepted")
	}
	if _, err := s.Breakdown(ctx, FilterDTO{}, "color"); err == nil {
		t.Error("unknown dimension accepted")
	}
}

func TestTimeSeriesFillsGapsAndStacks(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	insert(t, st,
		rec("a", day(-3, 10), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s", 100, 0, 0),
		rec("b", day(-1, 10), model.SourceCodexCLI, "openai", "gpt-5.6-sol", "/p", "s", 40, 0, 0),
		rec("c", day(0, 10), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s", 10, 0, 0),
	)
	f := FilterDTO{FromMs: day(-4, 0).UnixMilli(), ToMs: day(1, 0).UnixMilli()}

	plain, err := s.TimeSeries(ctx, f, "day", "")
	if err != nil {
		t.Fatal(err)
	}
	// days -4 .. 0 (the range ends tomorrow, but "now" is today).
	if len(plain.Buckets) != 5 {
		t.Fatalf("buckets = %d, want 5: %v", len(plain.Buckets), plain.Buckets)
	}
	for i, b := range plain.Buckets {
		if b != day(-4+i, 0).UnixMilli() {
			t.Errorf("bucket %d = %d, want local midnight", i, b)
		}
	}
	want := []int64{0, 100, 0, 40, 10}
	if len(plain.Series) != 1 {
		t.Fatalf("series = %d", len(plain.Series))
	}
	for i, v := range plain.Series[0].Values {
		if v != want[i] {
			t.Errorf("value[%d] = %d, want %d", i, v, want[i])
		}
	}

	stacked, err := s.TimeSeries(ctx, f, "day", "service")
	if err != nil {
		t.Fatal(err)
	}
	if len(stacked.Series) != 2 || stacked.OtherKey != "" {
		t.Fatalf("stacked layers = %+v other=%q", stacked.Series, stacked.OtherKey)
	}
	sumAt := func(i int) (n int64) {
		for _, sr := range stacked.Series {
			n += sr.Values[i]
		}
		return
	}
	for i := range want {
		if sumAt(i) != want[i] {
			t.Errorf("layers at %d sum to %d, want %d", i, sumAt(i), want[i])
		}
	}
}

func TestTimeSeriesFoldsOtherLayer(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	var recs []model.UsageRecord
	for i := 0; i < stackTop+2; i++ {
		id := string(rune('a' + i))
		recs = append(recs, rec(id, day(0, 8), model.SourceClaudeCode, "anthropic", "m-"+id, "/p", "s", int64(100-i), 0, 0))
	}
	insert(t, st, recs...)
	got, err := s.TimeSeries(ctx, FilterDTO{FromMs: day(0, 0).UnixMilli(), ToMs: day(1, 0).UnixMilli()}, "day", "model")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Series) != stackTop+1 || got.OtherKey != OtherKey {
		t.Fatalf("layers = %d other=%q", len(got.Series), got.OtherKey)
	}
	last := got.Series[len(got.Series)-1]
	if last.Key != OtherKey || last.Values[0] != int64(100-6)+int64(100-7) {
		t.Errorf("other layer = %+v", last)
	}
}

func TestBreakdownCompareSessions(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	reported := 0.5
	r4 := rec("r4", day(0, 11), model.SourceAnthropicAPI, "anthropic", "claude-opus-5-5", "/q", "s-rep", 10, 10, 0)
	r4.ReportedCostUSD = &reported
	insert(t, st,
		rec("r1", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s1", 1000, 0, 0),
		rec("r2", day(0, 10), model.SourceClaudeCode, "anthropic", "claude-opus-5-5", "/p", "s1", 500, 0, 0),
		rec("r3", day(0, 10), model.SourceCodexCLI, "openai", "unpriced-model", "/p", "s2", 70, 0, 0),
		r4,
	)

	bd, err := s.Breakdown(ctx, FilterDTO{}, "service")
	if err != nil {
		t.Fatal(err)
	}
	if len(bd.Rows) != 2 || bd.Rows[0].Key != "anthropic" || bd.Rows[0].Totals.TotalTokens != 1520 {
		t.Fatalf("service breakdown = %+v", bd.Rows)
	}
	if bd.Rows[0].Totals.CostUSD == nil || *bd.Rows[0].Totals.CostUSD <= reported {
		t.Errorf("anthropic cost = %v, want estimate + reported", bd.Rows[0].Totals.CostUSD)
	}
	if got := bd.Rows[1].Totals.UnknownModels; len(got) != 1 || got[0] != "unpriced-model" {
		t.Errorf("unknown models = %v", got)
	}

	cmp, err := s.Compare(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if len(cmp.Rows) != 3 || cmp.PriceAsOf == "" {
		t.Fatalf("compare rows = %+v", cmp.Rows)
	}
	for _, r := range cmp.Rows {
		if r.Model == "claude-opus-5-5" && (r.Totals.CostUSD == nil || *r.Totals.CostUSD != reported+opusCost(t, st)) {
			t.Errorf("opus compare cost = %v", r.Totals.CostUSD)
		}
	}

	se, err := s.Sessions(ctx, FilterDTO{}, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(se.Rows) != 2 || !se.Truncated {
		t.Fatalf("sessions = %+v truncated=%v", se.Rows, se.Truncated)
	}
	// Every session is priced; one whose records report their cost uses it.
	all, err := s.Sessions(ctx, FilterDTO{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if all.CostTruncated {
		t.Error("CostTruncated set under the cap")
	}
	for _, r := range all.Rows {
		if r.Totals.CostUSD == nil {
			t.Errorf("session %s has no cost", r.Key)
			continue
		}
		switch r.Key {
		case "s-rep":
			if *r.Totals.CostUSD != reported || r.Totals.CostEstimated {
				t.Errorf("reported session cost = %v estimated=%v", *r.Totals.CostUSD, r.Totals.CostEstimated)
			}
		case "s1": // sonnet 1000 in + opus 500 in
			want := sonnetInCost(t, st) + opusCost(t, st)
			if diff(*r.Totals.CostUSD, want) > 1e-9 || !r.Totals.CostEstimated {
				t.Errorf("s1 cost = %v, want %v", *r.Totals.CostUSD, want)
			}
		}
	}
	proj, err := s.Sessions(ctx, FilterDTO{Projects: []string{"/q"}}, 0)
	if err != nil || len(proj.Rows) != 1 || proj.Rows[0].Key != "s-rep" {
		t.Errorf("project-filtered sessions = %+v err=%v", proj.Rows, err)
	}
}

func sonnetInCost(t *testing.T, st *store.Store) float64 {
	t.Helper()
	p, err := pricing.Load(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	usd, _, _ := p.Cost(rec("x", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s1", 1000, 0, 0))
	return usd
}

func opusCost(t *testing.T, st *store.Store) float64 {
	t.Helper()
	p, err := pricing.Load(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	usd, _, _ := p.Cost(rec("x", day(0, 10), model.SourceClaudeCode, "anthropic", "claude-opus-5-5", "/p", "s1", 500, 0, 0))
	return usd
}

func TestTierUncertainIsReported(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	// Haiku 5.5 has a long-prompt tier; an API bucket has no request count.
	r := rec("h", day(0, 9), model.SourceAnthropicAPI, "anthropic", "claude-haiku-5-5", "", "", 200_000, 0, 0)
	r.RequestCount = 0
	insert(t, st, r)
	got, err := s.Summary(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Today.TierUncertainModels) != 1 || got.Today.TierUncertainModels[0] != "claude-haiku-5-5" {
		t.Errorf("TierUncertainModels = %v", got.Today.TierUncertainModels)
	}
}

func TestLimits(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	insert(t, st,
		rec("in5h", testNow.Add(-time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s", 300, 0, 0),
		rec("old", testNow.Add(-6*time.Hour), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s", 50, 0, 0),
	)

	empty, err := s.Limits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(empty.Limits) != 0 || len(empty.Current) != 2 {
		t.Fatalf("limits=%d current=%d", len(empty.Limits), len(empty.Current))
	}
	if empty.Current[0].Kind != "5h" || empty.Current[0].Totals.TotalTokens != 300 {
		t.Errorf("5h current = %+v", empty.Current[0])
	}
	fiveStart, _ := limits.FiveHourWindow(testNow)
	if empty.Current[0].StartMs != fiveStart.UnixMilli() {
		t.Errorf("5h start = %d", empty.Current[0].StartMs)
	}
	if empty.Current[1].Kind != "weekly" || empty.Current[1].Totals.TotalTokens != 350 {
		t.Errorf("weekly current = %+v", empty.Current[1])
	}

	if _, err := st.SetLimitWindow(ctx, store.LimitWindow{Kind: store.LimitKind5h, Limit: 1000, Metric: store.MetricTotalTokens}); err != nil {
		t.Fatal(err)
	}
	got, err := s.Limits(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Limits) != 1 {
		t.Fatalf("limits = %d", len(got.Limits))
	}
	l := got.Limits[0]
	if l.Used != 300 || l.Limit != 1000 || l.Ratio != 0.3 || !l.Estimated || l.Kind != "5h" {
		t.Errorf("limit = %+v", l)
	}
}

func TestMeta(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st, &fakeCollector{name: model.SourceClaudeCode}, &fakeCollector{name: model.SourceAnthropicAPI})
	s.hasKey = func(string) (bool, error) { return false, errors.New("keychain unavailable") }
	insert(t, st,
		rec("a", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s", 1, 0, 0),
		rec("b", day(0, 9), model.SourceManual, "openai", "gpt-5.6-sol", "", "", 1, 0, 0),
	)
	m, err := s.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 1, 0, 0, 0, 0, testLoc).UnixMilli(); m.FirstRecordMs != want {
		t.Errorf("FirstRecordMs = %d, want %d", m.FirstRecordMs, want)
	}
	if m.TotalRecords != 2 || len(m.Services) != 2 || len(m.Models) != 2 || len(m.Projects) != 2 {
		t.Errorf("meta = %+v", m)
	}
	if m.LastScanMs != 0 || m.Scanning {
		t.Errorf("fresh meta: lastScan=%d scanning=%v", m.LastScanMs, m.Scanning)
	}
	byID := map[string]SourceDTO{}
	for _, src := range m.Sources {
		byID[src.ID] = src
	}
	if len(m.Sources) != len(sources) {
		t.Errorf("sources = %d", len(m.Sources))
	}
	if c := byID["claude_code"]; !c.Collector || !c.Active || c.Records != 1 {
		t.Errorf("claude_code = %+v", c)
	}
	if a := byID["anthropic_api"]; !a.Collector || a.Active || !a.Enabled {
		t.Errorf("anthropic_api without a key = %+v", a)
	}
	if o := byID["codex_cli"]; o.Collector || o.Active {
		t.Errorf("unregistered codex_cli = %+v", o)
	}
	if mn := byID["manual"]; mn.Collector || mn.Records != 1 {
		t.Errorf("manual = %+v", mn)
	}
}

func TestAlignAndNextBucket(t *testing.T) {
	cases := []struct {
		b    store.Bucket
		in   time.Time
		want time.Time
		next time.Time
	}{
		{store.BucketHour, time.Date(2026, 11, 6, 15, 42, 30, 0, testLoc), time.Date(2026, 11, 6, 15, 0, 0, 0, testLoc), time.Date(2026, 11, 6, 16, 0, 0, 0, testLoc)},
		{store.BucketDay, time.Date(2026, 11, 6, 15, 42, 0, 0, testLoc), time.Date(2026, 11, 6, 0, 0, 0, 0, testLoc), time.Date(2026, 11, 7, 0, 0, 0, 0, testLoc)},
		{store.BucketWeek, time.Date(2026, 11, 6, 15, 0, 0, 0, testLoc), time.Date(2026, 11, 2, 0, 0, 0, 0, testLoc), time.Date(2026, 11, 9, 0, 0, 0, 0, testLoc)},
		{store.BucketMonth, time.Date(2026, 11, 30, 23, 0, 0, 0, testLoc), time.Date(2026, 11, 1, 0, 0, 0, 0, testLoc), time.Date(2026, 12, 1, 0, 0, 0, 0, testLoc)},
	}
	for _, c := range cases {
		got := alignBucket(c.in, c.b, testLoc)
		if !got.Equal(c.want) {
			t.Errorf("align %s = %v, want %v", c.b, got, c.want)
		}
		if n := nextBucket(got, c.b, testLoc); !n.Equal(c.next) {
			t.Errorf("next %s = %v, want %v", c.b, n, c.next)
		}
	}
}

func TestScanIDsAndMetaSnapshot(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	started, release := make(chan struct{}), make(chan struct{})
	first := true
	c := &fakeCollector{name: model.SourceClaudeCode, run: func(context.Context) (collector.Result, error) {
		if first {
			first = false
			close(started)
			<-release
		}
		return collector.Result{Added: 2}, nil
	}}
	s := newService(t, st, c)

	m, err := s.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Scanning || m.ScanID != 0 || m.LastScan != nil {
		t.Fatalf("idle meta = scanning %v id %d last %v", m.Scanning, m.ScanID, m.LastScan)
	}

	id, ok := s.BeginScan()
	if !ok || id != 1 {
		t.Fatalf("BeginScan = %d,%v, want 1,true", id, ok)
	}
	if id2, ok2 := s.BeginScan(); ok2 || id2 != 1 {
		t.Errorf("BeginScan while running = %d,%v, want 1,false", id2, ok2)
	}
	var steps []ScanStep
	done := make(chan ScanSummaryDTO)
	go func() { done <- s.RunScan(ctx, id, func(st ScanStep) { steps = append(steps, st) }) }()
	<-started

	m, err = s.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Scanning || m.ScanID != 1 || m.LastScan != nil {
		t.Errorf("running meta = scanning %v id %d last %v", m.Scanning, m.ScanID, m.LastScan)
	}
	close(release)
	sum := <-done
	if sum.ID != 1 {
		t.Errorf("summary id = %d", sum.ID)
	}
	for _, st := range steps {
		if st.ScanID != 1 {
			t.Errorf("step scan id = %d", st.ScanID)
		}
	}

	// Once RunScan has returned, Meta no longer reports the scan as running
	// and carries its summary: a Meta read before this point is the stale one.
	m, err = s.Meta(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.Scanning || m.ScanID != 0 || m.LastScan == nil || m.LastScan.ID != 1 || m.LastScan.Added != 2 {
		t.Errorf("finished meta = scanning %v id %d last %+v", m.Scanning, m.ScanID, m.LastScan)
	}

	id, ok = s.BeginScan()
	if !ok || id != 2 {
		t.Errorf("second BeginScan = %d,%v, want 2,true", id, ok)
	}
	s.RunScan(ctx, id, nil)
}

func TestSessionFilterAndCostCap(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)
	insert(t, st,
		rec("a", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s1", 1000, 0, 0),
		rec("b", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-opus-5-5", "/p", "s1", 500, 0, 0),
		rec("c", day(0, 9), model.SourceClaudeCode, "anthropic", "claude-opus-5-5", "/p", "s2", 70, 0, 0),
	)
	// Model breakdown of one session.
	bd, err := s.Breakdown(ctx, FilterDTO{Sessions: []string{"s1"}}, "model")
	if err != nil {
		t.Fatal(err)
	}
	if len(bd.Rows) != 2 || bd.Rows[0].Totals.TotalTokens != 1000 || bd.Rows[1].Totals.TotalTokens != 500 {
		t.Fatalf("s1 models = %+v", bd.Rows)
	}

	old := maxCostRows
	maxCostRows = 1
	defer func() { maxCostRows = old }()
	se, err := s.Sessions(ctx, FilterDTO{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !se.CostTruncated || se.Rows[0].Totals.CostUSD == nil || se.Rows[1].Totals.CostUSD != nil {
		t.Errorf("cost cap: truncated=%v rows=%+v", se.CostTruncated, se.Rows)
	}
}

func TestTotalOnlyCostIsUnavailable(t *testing.T) {
	ctx := context.Background()
	st := openStore(t)
	s := newService(t, st)

	totalOnly := rec("t1", day(0, 9), model.SourceCodexCLI, "openai", "gpt-5.5", "/p", "s1", 0, 0, 0)
	totalOnly.TotalOnly, totalOnly.TotalOnlyTokens = true, 4000
	split := rec("c1", day(0, 10), model.SourceClaudeCode, "anthropic", "claude-sonnet-5-5", "/p", "s2", 1000, 500, 0)
	insert(t, st, totalOnly, split)

	// Per model: the total-only model has no cost at all ("not computable"),
	// the split model is priced as usual.
	bd, err := s.Breakdown(ctx, FilterDTO{}, "model")
	if err != nil {
		t.Fatal(err)
	}
	byKey := map[string]TotalsDTO{}
	for _, r := range bd.Rows {
		byKey[r.Key] = r.Totals
	}
	g := byKey["gpt-5.5"]
	if g.CostUSD != nil || !g.CostUnavailable || g.CostPartial || g.TotalOnlyTokens != 4000 ||
		g.TotalOnlyRecords != 1 || g.TotalTokens != 4000 || g.UncostedTotalOnlyTokens != 4000 || len(g.UnknownModels) != 0 {
		t.Errorf("total-only model = %+v; want no cost, costUnavailable, 4000 tokens", g)
	}
	c := byKey["claude-sonnet-5-5"]
	if c.CostUSD == nil || *c.CostUSD <= 0 || c.CostUnavailable || c.CostPartial || c.TotalOnlyTokens != 0 {
		t.Errorf("split model = %+v; want a normal cost", c)
	}

	// Mixed set: the cost is a lower bound and says so.
	sum, err := s.Summary(ctx, FilterDTO{})
	if err != nil {
		t.Fatal(err)
	}
	d := sum.Today
	if d.CostUSD == nil || *d.CostUSD != *c.CostUSD || !d.CostPartial || d.CostUnavailable ||
		d.UncostedTotalOnlyTokens != 4000 || d.TotalTokens != 5500 || d.TotalOnlyTokens != 4000 {
		t.Errorf("today = %+v", d)
	}

	// Filtered to the total-only source alone, the whole set is unavailable.
	only, err := s.Summary(ctx, FilterDTO{Sources: []string{string(model.SourceCodexCLI)}})
	if err != nil {
		t.Fatal(err)
	}
	if only.Today.CostUSD != nil || !only.Today.CostUnavailable || only.Today.TotalTokens != 4000 {
		t.Errorf("total-only source today = %+v; want costUnavailable and no cost", only.Today)
	}
}
