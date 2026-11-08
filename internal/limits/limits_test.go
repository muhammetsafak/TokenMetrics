package limits

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/pricing"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "l.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

func newCalc(t *testing.T, st *store.Store) *Calculator {
	t.Helper()
	p, err := pricing.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return New(st, p)
}

func put(t *testing.T, st *store.Store, recs ...model.UsageRecord) {
	t.Helper()
	if _, _, err := st.InsertBatch(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
}

func rec(id string, ts time.Time, service, modelName string, in, out int64) model.UsageRecord {
	return model.UsageRecord{
		Source: model.SourceClaudeCode, ExternalID: id, Timestamp: ts,
		Service: service, Model: modelName, InputTokens: in, OutputTokens: out, RequestCount: 1,
	}
}

func setLimit(t *testing.T, st *store.Store, l store.LimitWindow) store.LimitWindow {
	t.Helper()
	id, err := st.SetLimitWindow(context.Background(), l)
	if err != nil {
		t.Fatal(err)
	}
	l.ID = id
	return l
}

var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC) // a Friday

func TestFiveHourWindowBoundary(t *testing.T) {
	st := openStore(t)
	ms := time.Millisecond
	start := now.Add(-5 * time.Hour) // 07:00:00.000
	put(t, st,
		rec("before", start.Add(-ms), "anthropic", "claude-sonnet-5-5", 1, 0), // 1 ms outside
		rec("at-start", start, "anthropic", "claude-sonnet-5-5", 10, 0),       // start is inclusive
		rec("inside", start.Add(time.Hour), "anthropic", "claude-sonnet-5-5", 100, 0),
		rec("at-now", now, "anthropic", "claude-sonnet-5-5", 1000, 0),          // now itself counts
		rec("future", now.Add(ms), "anthropic", "claude-sonnet-5-5", 10000, 0), // after now: not yet
	)
	lw := setLimit(t, st, store.LimitWindow{Kind: store.LimitKind5h, Metric: store.MetricTotalTokens, Limit: 2220})

	r, err := newCalc(t, st).Evaluate(context.Background(), lw, now)
	if err != nil {
		t.Fatal(err)
	}
	if r.Used != 1110 { // 10 + 100 + 1000
		t.Fatalf("Used = %v, want 1110", r.Used)
	}
	if r.Limit != 2220 || r.Ratio != 0.5 {
		t.Fatalf("Limit = %v Ratio = %v, want 2220 and 0.5", r.Limit, r.Ratio)
	}
	if !r.WindowStart.Equal(start) || !r.WindowEnd.Equal(now) || !r.Estimated {
		t.Fatalf("window = %v..%v estimated=%v", r.WindowStart, r.WindowEnd, r.Estimated)
	}
	if r.Kind != "5h" || r.Metric != store.MetricTotalTokens {
		t.Fatalf("kind/metric = %q/%q", r.Kind, r.Metric)
	}
}

func TestMetrics(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	r := rec("a", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 1_000_000, 500_000)
	r.CacheReadTokens = 2_000_000
	put(t, st, r)
	c := newCalc(t, st)

	total := setLimit(t, st, store.LimitWindow{Service: "anthropic", Kind: store.LimitKind5h, Metric: store.MetricTotalTokens, Limit: 7_000_000})
	res, err := c.Evaluate(ctx, total, now)
	if err != nil || res.Used != 3_500_000 || res.Ratio != 0.5 {
		t.Fatalf("total tokens: %+v err=%v", res, err)
	}

	out := setLimit(t, st, store.LimitWindow{Service: "anthropic", Kind: store.LimitKindWeekly, Metric: store.MetricOutputTokens, Limit: 1_000_000})
	res, err = c.Evaluate(ctx, out, now)
	if err != nil || res.Used != 500_000 || res.Ratio != 0.5 {
		t.Fatalf("output tokens: %+v err=%v", res, err)
	}

	// sonnet-5-5: 1M*2 + 0.5M*10 + 2M*0.10 = 7.20 USD; limit 1440 cents = 14.40 USD.
	cost := setLimit(t, st, store.LimitWindow{Service: "anthropic", Kind: store.LimitKind5h, Metric: store.MetricCostUSD, Limit: 1440})
	// This replaced the 5h row of the same service; cost is evaluated from the struct.
	res, err = c.Evaluate(ctx, cost, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Used-7.20) > 1e-9 || math.Abs(res.Limit-14.40) > 1e-9 || math.Abs(res.Ratio-0.5) > 1e-9 {
		t.Fatalf("cost: %+v", res)
	}
	if len(res.UnknownModels) != 0 {
		t.Fatalf("UnknownModels = %v", res.UnknownModels)
	}
}

func TestCostMetricReportedAndUnknownModels(t *testing.T) {
	st := openStore(t)
	rep := rec("reported", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 1_000_000, 0)
	cost := 3.0
	rep.ReportedCostUSD = &cost // wins over the 2.00 the table would give
	put(t, st, rep,
		rec("est", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 1_000_000, 0), // 2.00
		rec("unk", now.Add(-time.Hour), "anthropic", "mystery-model", 5_000_000, 0),     // no price
	)
	lw := setLimit(t, st, store.LimitWindow{Kind: store.LimitKind5h, Metric: store.MetricCostUSD, Limit: 1000})
	res, err := newCalc(t, st).Evaluate(context.Background(), lw, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Used-5.0) > 1e-9 || math.Abs(res.Ratio-0.5) > 1e-9 {
		t.Fatalf("Used = %v Ratio = %v, want 5 and 0.5", res.Used, res.Ratio)
	}
	if len(res.UnknownModels) != 1 || res.UnknownModels[0] != "mystery-model" {
		t.Fatalf("UnknownModels = %v", res.UnknownModels)
	}
}

func TestCostMetricFlagsUnknownRequestCountTier(t *testing.T) {
	st := openStore(t)
	// A daily API bucket of claude-haiku-5-5: 1M prompt tokens, request count unknown.
	bucket := rec("bucket", now.Add(-time.Hour), "anthropic", "claude-haiku-5-5", 1_000_000, 0)
	bucket.RequestCount = 0
	put(t, st, bucket)
	lw := setLimit(t, st, store.LimitWindow{Kind: store.LimitKind5h, Metric: store.MetricCostUSD, Limit: 20}) // 0.20 USD
	res, err := newCalc(t, st).Evaluate(context.Background(), lw, now)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(res.Used-0.10) > 1e-9 { // standard tier: 1M * 0.10, not the long tier's 0.50
		t.Fatalf("Used = %v, want 0.10", res.Used)
	}
	if len(res.TierUncertainModels) != 1 || res.TierUncertainModels[0] != "claude-haiku-5-5" {
		t.Fatalf("TierUncertainModels = %v", res.TierUncertainModels)
	}
}

func TestServiceFilterAndAllServices(t *testing.T) {
	st := openStore(t)
	ts := now.Add(-time.Hour)
	put(t, st,
		rec("a", ts, "anthropic", "claude-sonnet-5-5", 100, 0),
		rec("o", ts, "openai", "gpt-5", 20, 0),
	)
	c := newCalc(t, st)
	ctx := context.Background()

	only := setLimit(t, st, store.LimitWindow{Service: "openai", Kind: store.LimitKind5h, Metric: store.MetricTotalTokens, Limit: 100})
	if r, _ := c.Evaluate(ctx, only, now); r.Used != 20 || r.Service != "openai" {
		t.Fatalf("service-filtered: %+v", r)
	}
	all := setLimit(t, st, store.LimitWindow{Kind: store.LimitKind5h, Metric: store.MetricTotalTokens, Limit: 100})
	if r, _ := c.Evaluate(ctx, all, now); r.Used != 120 {
		t.Fatalf("all services: %+v", r)
	}
}

func TestRatioCanExceedOne(t *testing.T) {
	st := openStore(t)
	put(t, st, rec("a", now.Add(-time.Minute), "anthropic", "claude-sonnet-5-5", 300, 0))
	lw := setLimit(t, st, store.LimitWindow{Kind: store.LimitKind5h, Metric: store.MetricTotalTokens, Limit: 100})
	r, _ := newCalc(t, st).Evaluate(context.Background(), lw, now)
	if r.Ratio != 3 {
		t.Fatalf("Ratio = %v, want 3", r.Ratio)
	}
}

func TestWeeklyWindowStart(t *testing.T) {
	if now.Weekday() != time.Friday {
		t.Fatalf("fixture drift: %v is a %v", now, now.Weekday())
	}
	utc := func(y int, m time.Month, d, h, min int) time.Time {
		return time.Date(y, m, d, h, min, 0, 0, time.UTC)
	}
	cases := []struct {
		name       string
		cfg        WeeklyStart
		now        time.Time
		start, end time.Time
	}{
		{"monday 09:00, already past", WeeklyStart{Weekday: 1, Hour: 9, Timezone: "UTC"}, now,
			utc(2026, 11, 2, 9, 0), utc(2026, 11, 9, 9, 0)},
		{"same weekday, hour not reached yet", WeeklyStart{Weekday: 5, Hour: 15, Timezone: "UTC"}, now,
			utc(2026, 10, 30, 15, 0), utc(2026, 11, 6, 15, 0)},
		{"same weekday, hour reached", WeeklyStart{Weekday: 5, Hour: 12, Timezone: "UTC"}, now,
			utc(2026, 11, 6, 12, 0), utc(2026, 11, 13, 12, 0)}, // now is exactly the start
		{"sunday crossing a month boundary", WeeklyStart{Weekday: 0, Hour: 0, Timezone: "UTC"}, utc(2026, 11, 3, 5, 0),
			utc(2026, 11, 1, 0, 0), utc(2026, 11, 8, 0, 0)},
		{"weekday after today wraps to last week", WeeklyStart{Weekday: 6, Hour: 0, Timezone: "UTC"}, now,
			utc(2026, 10, 31, 0, 0), utc(2026, 11, 7, 0, 0)},
	}
	for _, tc := range cases {
		start, end, err := WeeklyWindow(tc.now, tc.cfg)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !start.Equal(tc.start) || !end.Equal(tc.end) {
			t.Errorf("%s: got %v..%v, want %v..%v", tc.name, start, end, tc.start, tc.end)
		}
	}
}

func TestWeeklyWindowAcrossDST(t *testing.T) {
	// US daylight saving time ended on Sunday 2026-11-01.
	cfg := WeeklyStart{Weekday: 0, Hour: 0, Timezone: "America/New_York"}
	at := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	start, end, err := WeeklyWindow(at, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC); !start.Equal(want) { // 00:00 EDT
		t.Errorf("start = %v, want %v", start.UTC(), want)
	}
	if want := time.Date(2026, 11, 8, 5, 0, 0, 0, time.UTC); !end.Equal(want) { // 00:00 EST
		t.Errorf("end = %v, want %v", end.UTC(), want)
	}
	if end.Sub(start) != 169*time.Hour {
		t.Errorf("window length = %v, want 169h", end.Sub(start))
	}
}

func TestWeeklyBoundaryRecord(t *testing.T) {
	st := openStore(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, SettingWeeklyStart, WeeklyStart{Weekday: 1, Hour: 9, Timezone: "UTC"}); err != nil {
		t.Fatal(err)
	}
	start := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	put(t, st,
		rec("before", start.Add(-time.Millisecond), "anthropic", "claude-sonnet-5-5", 1, 0),
		rec("at-start", start, "anthropic", "claude-sonnet-5-5", 10, 0),
		rec("mid-week", start.Add(48*time.Hour), "anthropic", "claude-sonnet-5-5", 100, 0),
	)
	setLimit(t, st, store.LimitWindow{Kind: store.LimitKindWeekly, Metric: store.MetricTotalTokens, Limit: 1000})

	results, err := newCalc(t, st).EvaluateAll(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("got %d results", len(results))
	}
	r := results[0]
	if r.Used != 110 || r.Ratio != 0.11 {
		t.Fatalf("Used = %v Ratio = %v, want 110 and 0.11", r.Used, r.Ratio)
	}
	if !r.WindowStart.Equal(start) || !r.WindowEnd.Equal(start.AddDate(0, 0, 7)) {
		t.Fatalf("window = %v..%v", r.WindowStart, r.WindowEnd)
	}
}

func TestWeeklyDefaultWhenSettingMissing(t *testing.T) {
	st := openStore(t)
	c := newCalc(t, st)
	cfg, err := c.WeeklyStartSetting(context.Background())
	if err != nil || cfg != DefaultWeeklyStart() {
		t.Fatalf("cfg = %+v err=%v", cfg, err)
	}
	// Default is Monday 00:00 local; the window must start on a Monday.
	start, _, err := WeeklyWindow(now, cfg)
	if err != nil || start.Weekday() != time.Monday || start.Hour() != 0 {
		t.Fatalf("start = %v err=%v", start, err)
	}
}

func TestWeeklyStartValidation(t *testing.T) {
	for _, bad := range []WeeklyStart{
		{Weekday: 7}, {Weekday: -1}, {Hour: 24}, {Hour: -1}, {Timezone: "Not/AZone"},
	} {
		if err := bad.Validate(); err == nil {
			t.Errorf("%+v: want error", bad)
		}
	}
	if err := (WeeklyStart{Weekday: 6, Hour: 23, Timezone: "Europe/Istanbul"}).Validate(); err != nil {
		t.Errorf("valid config rejected: %v", err)
	}

	st := openStore(t)
	ctx := context.Background()
	if err := st.SetSetting(ctx, SettingWeeklyStart, WeeklyStart{Weekday: 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := newCalc(t, st).WeeklyStartSetting(ctx); err == nil {
		t.Error("invalid stored setting: want error")
	}
}

func TestEvaluateAllOrderAndEmpty(t *testing.T) {
	st := openStore(t)
	c := newCalc(t, st)
	ctx := context.Background()
	if got, err := c.EvaluateAll(ctx, now); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
	setLimit(t, st, store.LimitWindow{Service: "openai", Kind: store.LimitKind5h, Metric: store.MetricOutputTokens, Limit: 10})
	setLimit(t, st, store.LimitWindow{Service: "anthropic", Kind: store.LimitKind5h, Metric: store.MetricOutputTokens, Limit: 10})
	got, err := c.EvaluateAll(ctx, now)
	if err != nil || len(got) != 2 || got[0].Service != "anthropic" || got[1].Service != "openai" {
		t.Fatalf("got %+v err=%v", got, err)
	}
	for _, r := range got {
		if r.Used != 0 || r.Ratio != 0 || !r.Estimated {
			t.Errorf("empty-usage result: %+v", r)
		}
	}
}

func TestTotalOnlyTokensAreFlaggedAsMissing(t *testing.T) {
	st := openStore(t)
	to := rec("t1", now.Add(-time.Hour), "openai", "gpt-5.5", 0, 0)
	to.Source, to.TotalOnly, to.TotalOnlyTokens = model.SourceCodexCLI, true, 6_000_000
	put(t, st, rec("split", now.Add(-time.Hour), "openai", "gpt-5.5", 1_000_000, 200_000), to)
	calc := newCalc(t, st)
	ctx := context.Background()

	cases := []struct {
		metric   string
		limit    int64
		wantUsed float64
	}{
		{store.MetricTotalTokens, 10_000_000, 7_200_000}, // total-only tokens count: nothing missing
		{store.MetricOutputTokens, 1_000_000, 200_000},   // no output split: all 6M missing
		{store.MetricCostUSD, 10_000, 0},                 // Used not asserted: depends on the price table
	}
	for _, tc := range cases {
		lw := setLimit(t, st, store.LimitWindow{Service: "", Kind: store.LimitKind5h, Metric: tc.metric, Limit: tc.limit})
		res, err := calc.Evaluate(ctx, lw, now)
		if err != nil {
			t.Fatal(err)
		}
		wantMissing := int64(6_000_000)
		if tc.metric == store.MetricTotalTokens {
			wantMissing = 0
		}
		if res.MissingTotalOnlyTokens != wantMissing {
			t.Errorf("%s: MissingTotalOnlyTokens = %d, want %d", tc.metric, res.MissingTotalOnlyTokens, wantMissing)
		}
		if tc.metric != store.MetricCostUSD && res.Used != tc.wantUsed {
			t.Errorf("%s: Used = %v, want %v", tc.metric, res.Used, tc.wantUsed)
		}
	}
}
