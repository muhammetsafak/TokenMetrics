package store

import (
	"context"
	"reflect"
	"testing"
	"time"

	_ "time/tzdata" // DST test must not depend on the host zoneinfo

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

func utc(y int, m time.Month, d, h, min int) time.Time {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC)
}

func fp(v float64) *float64 { return &v }

// fixture seeds a known data set and returns the store.
//
//	id  ts (UTC)             source       service    model   project  session in  out cr cw5 cw1 think req cost
//	r1  2026-01-01 10:00     claude_code  anthropic  opus    /a       s1      100 50  10 5   1   7     1   0.50
//	r2  2026-01-01 22:30     claude_code  anthropic  sonnet  /a       s1      200 100 0  0   0   0     2   nil
//	r3  2026-01-02 08:15     codex_cli    openai     gpt     /b       s2      300 150 20 0   0   3     3   1.00
//	r4  2026-01-04 23:45     claude_code  anthropic  opus    /b       s3      400 200 0  0   0   0     4   nil
//	r5  2026-02-03 01:00     manual       google     gem     ""       ""      50  25  0  0   0   0     1   nil
func fixture(t *testing.T) *Store {
	t.Helper()
	s := openTestStore(t)
	mk := func(id string, src model.Source, ts time.Time, svc, mdl, proj, sess string,
		in, out, cr, cw5, cw1, think, req int64, cost *float64) model.UsageRecord {
		return model.UsageRecord{
			Source: src, ExternalID: id, DeviceID: "d", Timestamp: ts,
			Service: svc, Model: mdl, Project: proj, Session: sess,
			InputTokens: in, OutputTokens: out, CacheReadTokens: cr,
			CacheWrite5mTokens: cw5, CacheWrite1hTokens: cw1, ThinkingTokens: think,
			RequestCount: req, ReportedCostUSD: cost,
		}
	}
	recs := []model.UsageRecord{
		mk("r1", model.SourceClaudeCode, utc(2026, 1, 1, 10, 0), "anthropic", "opus", "/a", "s1", 100, 50, 10, 5, 1, 7, 1, fp(0.5)),
		mk("r2", model.SourceClaudeCode, utc(2026, 1, 1, 22, 30), "anthropic", "sonnet", "/a", "s1", 200, 100, 0, 0, 0, 0, 2, nil),
		mk("r3", model.SourceCodexCLI, utc(2026, 1, 2, 8, 15), "openai", "gpt", "/b", "s2", 300, 150, 20, 0, 0, 3, 3, fp(1.0)),
		mk("r4", model.SourceClaudeCode, utc(2026, 1, 4, 23, 45), "anthropic", "opus", "/b", "s3", 400, 200, 0, 0, 0, 0, 4, nil),
		mk("r5", model.SourceManual, utc(2026, 2, 3, 1, 0), "google", "gem", "", "", 50, 25, 0, 0, 0, 0, 1, nil),
	}
	added, _, err := s.InsertBatch(context.Background(), recs)
	if err != nil || added != 5 {
		t.Fatalf("seed: added=%d err=%v", added, err)
	}
	return s
}

func TestTotals(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()

	all, err := s.Totals(ctx, Filter{})
	if err != nil {
		t.Fatal(err)
	}
	want := Totals{
		RecordCount: 5, RequestCount: 11,
		InputTokens: 1050, OutputTokens: 525, CacheReadTokens: 30,
		CacheWrite5mTokens: 5, CacheWrite1hTokens: 1, ThinkingTokens: 10,
		ReportedCostUSD: 1.5, CostReportedRecords: 2,
	}
	if all != want {
		t.Fatalf("all:\n got %+v\nwant %+v", all, want)
	}
	if got := all.TotalTokens(); got != 1050+525+30+5+1 {
		t.Fatalf("TotalTokens = %d", got)
	}

	cases := []struct {
		name         string
		f            Filter
		records      int64
		input, reqs  int64
		reportedCost float64
	}{
		{"source", Filter{Sources: []model.Source{model.SourceClaudeCode}}, 3, 700, 7, 0.5},
		{"two sources", Filter{Sources: []model.Source{model.SourceCodexCLI, model.SourceManual}}, 2, 350, 4, 1.0},
		{"service", Filter{Services: []string{"anthropic"}}, 3, 700, 7, 0.5},
		{"model", Filter{Models: []string{"opus"}}, 2, 500, 5, 0.5},
		{"project", Filter{Projects: []string{"/b"}}, 2, 700, 7, 1.0},
		{"empty project value", Filter{Projects: []string{""}}, 1, 50, 1, 0},
		{"session", Filter{Sessions: []string{"s1"}}, 2, 300, 3, 0.5},
		{"two sessions", Filter{Sessions: []string{"s2", "s3"}}, 2, 700, 7, 1.0},
		{"empty session value", Filter{Sessions: []string{""}}, 1, 50, 1, 0},
		{"session and project", Filter{Sessions: []string{"s1"}, Projects: []string{"/b"}}, 0, 0, 0, 0},
		{"combined", Filter{Services: []string{"anthropic"}, Projects: []string{"/b"}}, 1, 400, 4, 0},
		{"From inclusive", Filter{From: utc(2026, 1, 2, 8, 15)}, 3, 750, 8, 1.0},
		{"To exclusive", Filter{To: utc(2026, 1, 2, 8, 15)}, 2, 300, 3, 0.5},
		{"range", Filter{From: utc(2026, 1, 2, 0, 0), To: utc(2026, 1, 5, 0, 0)}, 2, 700, 7, 1.0},
		{"no match", Filter{Models: []string{"nope"}}, 0, 0, 0, 0},
	}
	for _, c := range cases {
		got, err := s.Totals(ctx, c.f)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if got.RecordCount != c.records || got.InputTokens != c.input || got.RequestCount != c.reqs || got.ReportedCostUSD != c.reportedCost {
			t.Errorf("%s: got %+v", c.name, got)
		}
	}
}

func TestTotalsFilterIsParameterized(t *testing.T) {
	s := fixture(t)
	got, err := s.Totals(context.Background(), Filter{Models: []string{"opus' OR '1'='1"}})
	if err != nil || got.RecordCount != 0 {
		t.Fatalf("injection-like value matched: %+v err=%v", got, err)
	}
}

type pt struct {
	start time.Time
	in    int64
	recs  int64
}

func points(t *testing.T, s *Store, f Filter, b Bucket, loc *time.Location) []pt {
	t.Helper()
	ps, err := s.TimeSeries(context.Background(), f, b, loc)
	if err != nil {
		t.Fatalf("TimeSeries(%s): %v", b, err)
	}
	out := make([]pt, len(ps))
	for i, p := range ps {
		out[i] = pt{p.Start, p.InputTokens, p.RecordCount}
	}
	return out
}

func samePoints(a, b []pt) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !a[i].start.Equal(b[i].start) || a[i].in != b[i].in || a[i].recs != b[i].recs {
			return false
		}
	}
	return true
}

func TestTimeSeriesDayUTC(t *testing.T) {
	s := fixture(t)
	got := points(t, s, Filter{}, BucketDay, nil)
	want := []pt{
		{utc(2026, 1, 1, 0, 0), 300, 2},
		{utc(2026, 1, 2, 0, 0), 300, 1},
		{utc(2026, 1, 4, 0, 0), 400, 1},
		{utc(2026, 2, 3, 0, 0), 50, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestTimeSeriesDayFollowsLocation(t *testing.T) {
	s := fixture(t)
	tr := time.FixedZone("UTC+3", 3*3600)
	// r2 (22:30Z) and r4 (23:45Z) roll over into the next local day.
	got := points(t, s, Filter{}, BucketDay, tr)
	want := []pt{
		{time.Date(2026, 1, 1, 0, 0, 0, 0, tr), 100, 1},
		{time.Date(2026, 1, 2, 0, 0, 0, 0, tr), 500, 2},
		{time.Date(2026, 1, 5, 0, 0, 0, 0, tr), 400, 1},
		{time.Date(2026, 2, 3, 0, 0, 0, 0, tr), 50, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
	ps, _ := s.TimeSeries(context.Background(), Filter{}, BucketDay, tr)
	if ps[0].Start.Location() != tr {
		t.Fatalf("Start location = %v, want %v", ps[0].Start.Location(), tr)
	}
}

func TestTimeSeriesHour(t *testing.T) {
	s := fixture(t)
	got := points(t, s, Filter{Services: []string{"anthropic"}, To: utc(2026, 1, 2, 0, 0)}, BucketHour, nil)
	want := []pt{
		{utc(2026, 1, 1, 10, 0), 100, 1},
		{utc(2026, 1, 1, 22, 0), 200, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}

	// Half-hour zone: local hours start at :30 UTC. 10:00Z is 15:30 IST (bucket 15:00) and
	// 22:30Z is exactly 04:00 IST (next day).
	ist := time.FixedZone("IST", 5*3600+1800)
	got = points(t, s, Filter{Services: []string{"anthropic"}, To: utc(2026, 1, 2, 0, 0)}, BucketHour, ist)
	want = []pt{
		{time.Date(2026, 1, 1, 15, 0, 0, 0, ist), 100, 1},
		{time.Date(2026, 1, 2, 4, 0, 0, 0, ist), 200, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("IST: got %v\nwant %v", got, want)
	}
}

func TestTimeSeriesWeekAndMonth(t *testing.T) {
	s := fixture(t)

	// 2026-01-01 is a Thursday; its Monday is 2025-12-29. 2026-01-04 is a Sunday
	// (still that week). 2026-02-03 is a Tuesday; its Monday is 2026-02-02.
	got := points(t, s, Filter{}, BucketWeek, nil)
	want := []pt{
		{utc(2025, 12, 29, 0, 0), 1000, 4},
		{utc(2026, 2, 2, 0, 0), 50, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("week: got %v\nwant %v", got, want)
	}

	got = points(t, s, Filter{}, BucketMonth, nil)
	want = []pt{
		{utc(2026, 1, 1, 0, 0), 1000, 4},
		{utc(2026, 2, 1, 0, 0), 50, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("month: got %v\nwant %v", got, want)
	}

	// UTC+3: r4 (Jan 4 23:45Z) becomes Monday Jan 5 local, a new week.
	tr := time.FixedZone("UTC+3", 3*3600)
	got = points(t, s, Filter{}, BucketWeek, tr)
	want = []pt{
		{time.Date(2025, 12, 29, 0, 0, 0, 0, tr), 600, 3},
		{time.Date(2026, 1, 5, 0, 0, 0, 0, tr), 400, 1},
		{time.Date(2026, 2, 2, 0, 0, 0, 0, tr), 50, 1},
	}
	if !samePoints(got, want) {
		t.Fatalf("week UTC+3: got %v\nwant %v", got, want)
	}
}

func TestTimeSeriesDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	s := openTestStore(t)
	// US DST began 2026-03-08 02:00 local (07:00Z). The local day is 23h long.
	recs := []model.UsageRecord{
		{Source: model.SourceManual, ExternalID: "a", Timestamp: utc(2026, 3, 8, 4, 59), InputTokens: 1},   // Mar 7 23:59 EST
		{Source: model.SourceManual, ExternalID: "b", Timestamp: utc(2026, 3, 8, 5, 0), InputTokens: 10},   // Mar 8 00:00 EST
		{Source: model.SourceManual, ExternalID: "c", Timestamp: utc(2026, 3, 9, 3, 59), InputTokens: 100}, // Mar 8 23:59 EDT
		{Source: model.SourceManual, ExternalID: "d", Timestamp: utc(2026, 3, 9, 4, 0), InputTokens: 1000}, // Mar 9 00:00 EDT
	}
	if _, _, err := s.InsertBatch(context.Background(), recs); err != nil {
		t.Fatal(err)
	}
	got := points(t, s, Filter{}, BucketDay, loc)
	want := []pt{
		{time.Date(2026, 3, 7, 0, 0, 0, 0, loc), 1, 1},
		{time.Date(2026, 3, 8, 0, 0, 0, 0, loc), 110, 2},
		{time.Date(2026, 3, 9, 0, 0, 0, 0, loc), 1000, 1},
	}
	// 04:59Z is in a 15-minute group starting 04:45Z, still Mar 7 local.
	if !samePoints(got, want) {
		t.Fatalf("got %v\nwant %v", got, want)
	}
}

func TestTimeSeriesEmptyAndInvalid(t *testing.T) {
	s := fixture(t)
	ps, err := s.TimeSeries(context.Background(), Filter{Models: []string{"nope"}}, BucketDay, nil)
	if err != nil || len(ps) != 0 {
		t.Fatalf("empty: %v err=%v", ps, err)
	}
	if _, err := s.TimeSeries(context.Background(), Filter{}, "year", nil); err == nil {
		t.Fatal("invalid bucket accepted")
	}
}

func TestTimeSeriesTotalsMatchTotals(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()
	ps, err := s.TimeSeries(ctx, Filter{}, BucketDay, time.FixedZone("x", -7*3600))
	if err != nil {
		t.Fatal(err)
	}
	var sum Totals
	for _, p := range ps {
		sum.add(p.Totals)
	}
	all, _ := s.Totals(ctx, Filter{})
	if sum != all {
		t.Fatalf("sum of buckets %+v != totals %+v", sum, all)
	}
}

func TestBreakdown(t *testing.T) {
	s := fixture(t)
	ctx := context.Background()

	type row struct {
		key   string
		total int64
		recs  int64
	}
	cases := []struct {
		dim  Dimension
		want []row
	}{
		{DimService, []row{{"anthropic", 100 + 50 + 10 + 5 + 1 + 300 + 600, 3}, {"openai", 470, 1}, {"google", 75, 1}}},
		{DimModel, []row{{"opus", 166 + 600, 2}, {"gpt", 470, 1}, {"sonnet", 300, 1}, {"gem", 75, 1}}},
		{DimProject, []row{{"/b", 470 + 600, 2}, {"/a", 166 + 300, 2}, {"", 75, 1}}},
		{DimSession, []row{{"s3", 600, 1}, {"s2", 470, 1}, {"s1", 466, 2}, {"", 75, 1}}},
		{DimSource, []row{{"claude_code", 166 + 300 + 600, 3}, {"codex_cli", 470, 1}, {"manual", 75, 1}}},
	}
	for _, c := range cases {
		rows, err := s.Breakdown(ctx, Filter{}, c.dim)
		if err != nil {
			t.Fatalf("%s: %v", c.dim, err)
		}
		got := make([]row, len(rows))
		for i, r := range rows {
			got[i] = row{r.Key, r.TotalTokens(), r.RecordCount}
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s:\n got %v\nwant %v", c.dim, got, c.want)
		}
	}

	// Filter applies to breakdowns too, and the row carries every counter.
	rows, err := s.Breakdown(ctx, Filter{Services: []string{"anthropic"}}, DimModel)
	if err != nil || len(rows) != 2 {
		t.Fatalf("filtered: %v err=%v", rows, err)
	}
	opus := rows[0]
	if opus.Key != "opus" || opus.InputTokens != 500 || opus.CacheReadTokens != 10 ||
		opus.CacheWrite5mTokens != 5 || opus.CacheWrite1hTokens != 1 || opus.ThinkingTokens != 7 ||
		opus.RequestCount != 5 || opus.ReportedCostUSD != 0.5 || opus.CostReportedRecords != 1 {
		t.Fatalf("opus row: %+v", opus)
	}

	if _, err := s.Breakdown(ctx, Filter{}, "model; DROP TABLE usage_records"); err == nil {
		t.Fatal("invalid dimension accepted")
	}
}
