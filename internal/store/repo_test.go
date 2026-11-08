package store

import (
	"context"
	"database/sql"
	"errors"
	"math"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

func ptrF(v float64) *float64 { return &v }
func ptrI(v int64) *int64     { return &v }

func TestPriceOverrideCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	if got, err := s.ListPriceOverrides(ctx); err != nil || len(got) != 0 {
		t.Fatalf("empty list = %v, %v", got, err)
	}

	// Partial override: unset components stay NULL (use the built-in price).
	if err := s.SetPriceOverride(ctx, PriceOverride{Model: " claude-x ", InputPerMTok: ptrF(1.5), CacheReadPerMTok: ptrF(0)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPriceOverride(ctx, PriceOverride{Model: "a-model", OutputPerMTok: ptrF(9)}); err != nil {
		t.Fatal(err)
	}
	got, err := s.ListPriceOverrides(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	if got[0].Model != "a-model" || got[1].Model != "claude-x" {
		t.Fatalf("not ordered by model / not trimmed: %q, %q", got[0].Model, got[1].Model)
	}
	x := got[1]
	if x.InputPerMTok == nil || *x.InputPerMTok != 1.5 || x.OutputPerMTok != nil ||
		x.CacheReadPerMTok == nil || *x.CacheReadPerMTok != 0 || // an explicit 0 is not NULL
		x.CacheWrite5mPerMTok != nil || x.CacheWrite1hPerMTok != nil {
		t.Fatalf("components = %+v", x)
	}
	if time.Since(x.UpdatedAt) > time.Minute || x.UpdatedAt.IsZero() {
		t.Fatalf("UpdatedAt = %v", x.UpdatedAt)
	}

	// Setting again replaces every component (a missing one becomes NULL again).
	if err := s.SetPriceOverride(ctx, PriceOverride{Model: "claude-x", OutputPerMTok: ptrF(7)}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.ListPriceOverrides(ctx)
	if len(got) != 2 || got[1].InputPerMTok != nil || got[1].OutputPerMTok == nil || *got[1].OutputPerMTok != 7 {
		t.Fatalf("after replace = %+v", got[1])
	}

	if err := s.DeletePriceOverride(ctx, "claude-x"); err != nil {
		t.Fatal(err)
	}
	if err := s.DeletePriceOverride(ctx, "claude-x"); err != nil {
		t.Fatalf("deleting a missing override must not fail: %v", err)
	}
	if got, _ := s.ListPriceOverrides(ctx); len(got) != 1 {
		t.Fatalf("after delete = %+v", got)
	}
}

func TestPriceOverrideValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	bad := []PriceOverride{
		{Model: "", InputPerMTok: ptrF(1)},
		{Model: "   ", InputPerMTok: ptrF(1)},
		{Model: "m"}, // no component
		{Model: "m", InputPerMTok: ptrF(-1)},
		{Model: "m", InputPerMTok: ptrF(math.NaN())},
		{Model: "m", OutputPerMTok: ptrF(math.Inf(1))},
	}
	for i, o := range bad {
		if err := s.SetPriceOverride(ctx, o); err == nil {
			t.Errorf("case %d (%+v): want error", i, o)
		}
	}
	if got, _ := s.ListPriceOverrides(ctx); len(got) != 0 {
		t.Fatalf("invalid overrides were stored: %+v", got)
	}
}

func TestLimitWindowCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id1, err := s.SetLimitWindow(ctx, LimitWindow{Service: "anthropic", Kind: LimitKind5h, Metric: MetricTotalTokens, Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	id2, err := s.SetLimitWindow(ctx, LimitWindow{Service: "anthropic", Kind: LimitKindWeekly, Metric: MetricCostUSD, Limit: 5000})
	if err != nil {
		t.Fatal(err)
	}
	if id1 == id2 {
		t.Fatal("different kinds must be different rows")
	}

	// Same (service, kind) updates in place instead of adding a row.
	id1b, err := s.SetLimitWindow(ctx, LimitWindow{Service: "anthropic", Kind: LimitKind5h, Metric: MetricOutputTokens, Limit: 2000})
	if err != nil || id1b != id1 {
		t.Fatalf("upsert id = %d, %v; want %d", id1b, err, id1)
	}
	got, err := s.ListLimitWindows(ctx)
	if err != nil || len(got) != 2 {
		t.Fatalf("list = %+v, %v", got, err)
	}
	// ordered by service, kind: "5h" < "weekly"
	if got[0].ID != id1 || got[0].Metric != MetricOutputTokens || got[0].Limit != 2000 || got[0].Service != "anthropic" {
		t.Fatalf("first = %+v", got[0])
	}
	if got[1].Kind != LimitKindWeekly || got[1].Limit != 5000 || got[1].UpdatedAt.IsZero() {
		t.Fatalf("second = %+v", got[1])
	}

	// An empty service (all services) is its own slot.
	if _, err := s.SetLimitWindow(ctx, LimitWindow{Kind: LimitKind5h, Metric: MetricTotalTokens, Limit: 1}); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListLimitWindows(ctx); len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}

	if err := s.DeleteLimitWindow(ctx, id1); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteLimitWindow(ctx, id1); err != nil {
		t.Fatalf("deleting a missing window must not fail: %v", err)
	}
	if got, _ := s.ListLimitWindows(ctx); len(got) != 2 {
		t.Fatalf("after delete len = %d, want 2", len(got))
	}
}

func TestLimitWindowValidation(t *testing.T) {
	s := openTestStore(t)
	for i, l := range []LimitWindow{
		{Kind: "daily", Metric: MetricTotalTokens, Limit: 1},
		{Kind: LimitKind5h, Metric: "bananas", Limit: 1},
		{Kind: LimitKind5h, Metric: MetricTotalTokens, Limit: 0},
		{Kind: LimitKind5h, Metric: MetricTotalTokens, Limit: -5},
	} {
		if _, err := s.SetLimitWindow(context.Background(), l); err == nil {
			t.Errorf("case %d (%+v): want error", i, l)
		}
	}
	if got, _ := s.ListLimitWindows(context.Background()); len(got) != 0 {
		t.Fatalf("invalid windows stored: %+v", got)
	}
}

func TestBudgetCRUD(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()

	id, err := s.CreateBudget(ctx, Budget{Scope: BudgetScopeService, ScopeValue: "openai", Period: BudgetPeriodMonthly,
		ThresholdTokens: ptrI(5000), ThresholdUSD: ptrF(12.5), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	// "total" drops any scope value; a disabled budget is listed only without the filter.
	id2, err := s.CreateBudget(ctx, Budget{Scope: BudgetScopeTotal, ScopeValue: "ignored", Period: BudgetPeriodDaily, ThresholdUSD: ptrF(1), Enabled: false})
	if err != nil {
		t.Fatal(err)
	}

	all, err := s.ListBudgets(ctx, false)
	if err != nil || len(all) != 2 {
		t.Fatalf("all = %+v, %v", all, err)
	}
	b := all[0]
	if b.ID != id || b.Scope != BudgetScopeService || b.ScopeValue != "openai" || b.Period != BudgetPeriodMonthly ||
		b.ThresholdTokens == nil || *b.ThresholdTokens != 5000 || b.ThresholdUSD == nil || *b.ThresholdUSD != 12.5 || !b.Enabled {
		t.Fatalf("budget = %+v", b)
	}
	if all[1].ID != id2 || all[1].ScopeValue != "" || all[1].ThresholdTokens != nil || all[1].Enabled {
		t.Fatalf("second budget = %+v", all[1])
	}
	if on, _ := s.ListBudgets(ctx, true); len(on) != 1 || on[0].ID != id {
		t.Fatalf("enabled only = %+v", on)
	}

	b.ThresholdTokens = nil
	b.Enabled = false
	if err := s.UpdateBudget(ctx, b); err != nil {
		t.Fatal(err)
	}
	all, _ = s.ListBudgets(ctx, false)
	if all[0].ThresholdTokens != nil || all[0].Enabled || all[0].ThresholdUSD == nil {
		t.Fatalf("after update = %+v", all[0])
	}
	if err := s.UpdateBudget(ctx, Budget{ID: 9999, Scope: BudgetScopeTotal, Period: BudgetPeriodDaily, ThresholdUSD: ptrF(1)}); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("update of a missing budget: %v, want sql.ErrNoRows", err)
	}
}

func TestBudgetValidation(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	bad := []Budget{
		{Scope: "team", Period: BudgetPeriodDaily, ThresholdUSD: ptrF(1)},
		{Scope: BudgetScopeTotal, Period: "weekly", ThresholdUSD: ptrF(1)},
		{Scope: BudgetScopeTotal, Period: BudgetPeriodDaily},                          // no threshold
		{Scope: BudgetScopeService, Period: BudgetPeriodDaily, ThresholdUSD: ptrF(1)}, // no scope value
		{Scope: BudgetScopeModel, Period: BudgetPeriodDaily, ThresholdTokens: ptrI(0)},
		{Scope: BudgetScopeTotal, Period: BudgetPeriodDaily, ThresholdTokens: ptrI(-1)},
		{Scope: BudgetScopeTotal, Period: BudgetPeriodDaily, ThresholdUSD: ptrF(0)},
		{Scope: BudgetScopeTotal, Period: BudgetPeriodDaily, ThresholdUSD: ptrF(math.NaN())},
	}
	for i, b := range bad {
		if _, err := s.CreateBudget(ctx, b); err == nil {
			t.Errorf("case %d (%+v): want error", i, b)
		}
	}
	if got, _ := s.ListBudgets(ctx, false); len(got) != 0 {
		t.Fatalf("invalid budgets stored: %+v", got)
	}
}

func TestNotificationsSent(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	id, err := s.CreateBudget(ctx, Budget{Scope: BudgetScopeTotal, Period: BudgetPeriodDaily, ThresholdUSD: ptrF(1), Enabled: true})
	if err != nil {
		t.Fatal(err)
	}

	if sent, err := s.NotificationSent(ctx, id, "2026-11-06"); err != nil || sent {
		t.Fatalf("before: sent=%v err=%v", sent, err)
	}
	if first, err := s.MarkNotificationSent(ctx, id, "2026-11-06", time.Now()); err != nil || !first {
		t.Fatalf("first mark: %v %v", first, err)
	}
	if first, err := s.MarkNotificationSent(ctx, id, "2026-11-06", time.Now()); err != nil || first {
		t.Fatalf("second mark: first=%v err=%v; want false, nil", first, err)
	}
	if sent, _ := s.NotificationSent(ctx, id, "2026-11-06"); !sent {
		t.Fatal("not recorded")
	}
	if sent, _ := s.NotificationSent(ctx, id, "2026-11-07"); sent {
		t.Fatal("a different period must not be marked")
	}

	// Unknown budget: foreign key rejects it.
	if _, err := s.MarkNotificationSent(ctx, 12345, "2026-11-06", time.Now()); err == nil {
		t.Fatal("marking an unknown budget: want error")
	}

	// Deleting the budget removes its notifications.
	if err := s.DeleteBudget(ctx, id); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM notifications_sent`).Scan(&n); err != nil || n != 0 {
		t.Fatalf("notifications left after budget delete: %d, %v", n, err)
	}
}

func TestCostInputs(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)

	mk := func(id, modelName string, in, out, cr, w5, w1 int64, cost *float64, at time.Time) model.UsageRecord {
		r := rec(id)
		r.Model, r.InputTokens, r.OutputTokens = modelName, in, out
		r.CacheReadTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens = cr, w5, w1
		r.ReportedCostUSD, r.Timestamp = cost, at
		return r
	}
	c := 0.75
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{
		mk("a1", "model-a", 10, 20, 30, 40, 50, nil, base),
		mk("a2", "model-a", 1, 2, 3, 4, 5, nil, base.Add(time.Hour)),
		mk("a3", "model-a", 1000, 1000, 1000, 1000, 1000, &c, base), // reported: tokens excluded, cost included
		mk("b1", "model-b", 7, 0, 0, 0, 0, &c, base),                // only reported
		mk("late", "model-a", 100, 0, 0, 0, 0, nil, base.Add(48*time.Hour)),
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.CostInputs(ctx, Filter{From: base, To: base.Add(24 * time.Hour)}, 0)
	if err != nil || len(got) != 2 {
		t.Fatalf("got %+v, %v", got, err)
	}
	a, b := got[0], got[1]
	if a.Model != "model-a" || a.InputTokens != 11 || a.OutputTokens != 22 || a.CacheReadTokens != 33 ||
		a.CacheWrite5mTokens != 44 || a.CacheWrite1hTokens != 55 {
		t.Fatalf("model-a tokens = %+v", a)
	}
	if a.UnreportedRecords != 2 || a.ReportedRecords != 1 || a.ReportedCostUSD != 0.75 {
		t.Fatalf("model-a counts = %+v", a)
	}
	if b.Model != "model-b" || b.UnreportedRecords != 0 || b.ReportedRecords != 1 || b.ReportedCostUSD != 0.75 || b.InputTokens != 0 {
		t.Fatalf("model-b = %+v", b)
	}

	// The filter applies: model-a only, whole range.
	got, err = s.CostInputs(ctx, Filter{Models: []string{"model-a"}}, 0)
	if err != nil || len(got) != 1 || got[0].InputTokens != 111 || got[0].UnreportedRecords != 3 {
		t.Fatalf("filtered = %+v, %v", got, err)
	}

	// No match: empty, not an error.
	if got, err := s.CostInputs(ctx, Filter{Models: []string{"nope"}}, 0); err != nil || len(got) != 0 {
		t.Fatalf("no match = %+v, %v", got, err)
	}
}

func TestCostInputsLongPrompt(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	base := time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC)
	c := 1.0

	mk := func(id string, in, cr, w5, w1, out, requests int64, cost *float64) model.UsageRecord {
		r := rec(id)
		r.Model, r.Timestamp, r.RequestCount, r.ReportedCostUSD = "m", base, requests, cost
		r.InputTokens, r.CacheReadTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens, r.OutputTokens = in, cr, w5, w1, out
		return r
	}
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{
		mk("short", 60_000, 40_000, 0, 0, 1, 1, nil),  // prompt 100,000: not over
		mk("long1", 60_000, 40_001, 0, 0, 2, 1, nil),  // prompt 100,001: over
		mk("long2", 0, 0, 100_000, 50_000, 4, 1, nil), // cache writes count: 150,000
		mk("agg-short", 150_000, 0, 0, 0, 8, 2, nil),  // average 75,000 per request: not over
		mk("agg-long", 300_000, 0, 0, 0, 16, 2, nil),  // average 150,000 per request: over
		mk("reported", 900_000, 0, 0, 0, 32, 1, &c),   // reported cost: never in the token sums
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.CostInputs(ctx, Filter{}, 100_000)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	g := got[0]
	wantAll := TokenCounts{InputTokens: 60_000 + 60_000 + 0 + 150_000 + 300_000, OutputTokens: 1 + 2 + 4 + 8 + 16,
		CacheReadTokens: 40_000 + 40_001, CacheWrite5mTokens: 100_000, CacheWrite1hTokens: 50_000}
	wantLong := TokenCounts{InputTokens: 60_000 + 0 + 300_000, OutputTokens: 2 + 4 + 16,
		CacheReadTokens: 40_001, CacheWrite5mTokens: 100_000, CacheWrite1hTokens: 50_000}
	if g.TokenCounts != wantAll {
		t.Errorf("all = %+v, want %+v", g.TokenCounts, wantAll)
	}
	if g.Long != wantLong {
		t.Errorf("long = %+v, want %+v", g.Long, wantLong)
	}
	if g.UnreportedRecords != 5 || g.ReportedRecords != 1 {
		t.Errorf("counts = %+v", g)
	}

	// A record with an unknown request count (0) is never long; it is counted
	// in TierUnknownRecords when its prompt is over the threshold.
	if _, _, err := s.InsertBatch(ctx, []model.UsageRecord{
		mk("bucket-big", 1_000_000, 0, 0, 0, 100, 0, nil),
		mk("bucket-small", 10_000, 0, 0, 0, 5, 0, nil),
	}); err != nil {
		t.Fatal(err)
	}
	got, err = s.CostInputs(ctx, Filter{}, 100_000)
	if err != nil || len(got) != 1 {
		t.Fatalf("with unknown-count records: %+v, %v", got, err)
	}
	if got[0].Long != wantLong {
		t.Errorf("unknown-count records leaked into Long: %+v, want %+v", got[0].Long, wantLong)
	}
	if got[0].TierUnknownRecords != 1 || got[0].UnreportedRecords != 7 {
		t.Errorf("TierUnknownRecords = %d, UnreportedRecords = %d; want 1 and 7", got[0].TierUnknownRecords, got[0].UnreportedRecords)
	}
	wantAll.InputTokens += 1_010_000
	wantAll.OutputTokens += 105
	if got[0].TokenCounts != wantAll {
		t.Errorf("all = %+v, want %+v", got[0].TokenCounts, wantAll)
	}
	// Remove them again so the checks below see the original data set.
	if _, err := s.db.Exec(`DELETE FROM usage_records WHERE external_id LIKE 'bucket-%'`); err != nil {
		t.Fatal(err)
	}
	wantAll.InputTokens -= 1_010_000
	wantAll.OutputTokens -= 105

	// Threshold 0 disables the split; the totals stay the same.
	got, err = s.CostInputs(ctx, Filter{}, 0)
	if err != nil || got[0].Long != (TokenCounts{}) || got[0].TokenCounts != wantAll {
		t.Fatalf("threshold 0: %+v, %v", got, err)
	}

	// The filter placeholders still line up with the threshold placeholders.
	got, err = s.CostInputs(ctx, Filter{Models: []string{"m"}, From: base, To: base.Add(time.Hour)}, 100_000)
	if err != nil || len(got) != 1 || got[0].Long != wantLong {
		t.Fatalf("filtered: %+v, %v", got, err)
	}
}

func TestPriceOverrideModelIsCaseInsensitive(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	if err := s.SetPriceOverride(ctx, PriceOverride{Model: "Claude-X", InputPerMTok: ptrF(1)}); err != nil {
		t.Fatal(err)
	}
	if err := s.SetPriceOverride(ctx, PriceOverride{Model: " claude-x", InputPerMTok: ptrF(2)}); err != nil {
		t.Fatal(err)
	}
	got, _ := s.ListPriceOverrides(ctx)
	if len(got) != 1 || got[0].Model != "claude-x" || *got[0].InputPerMTok != 2 {
		t.Fatalf("list = %+v, want a single lower-case claude-x override with input 2", got)
	}
	// Delete matches the same way (case and surrounding spaces).
	if err := s.DeletePriceOverride(ctx, "  CLAUDE-X "); err != nil {
		t.Fatal(err)
	}
	if got, _ := s.ListPriceOverrides(ctx); len(got) != 0 {
		t.Fatalf("override survived delete: %+v", got)
	}
}

func TestUpdateBudgetResetsNotificationsWhenRuleChanges(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	b := Budget{Scope: BudgetScopeTotal, Period: BudgetPeriodDaily, ThresholdUSD: ptrF(10), Enabled: true}
	id, err := s.CreateBudget(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	b.ID = id
	sent := func() bool {
		ok, err := s.NotificationSent(ctx, id, "2026-11-06")
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}
	mark := func() {
		t.Helper()
		if _, err := s.MarkNotificationSent(ctx, id, "2026-11-06", time.Now()); err != nil {
			t.Fatal(err)
		}
	}

	mark()
	// Saving the same rule, or only toggling Enabled, keeps the notification.
	if err := s.UpdateBudget(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.Enabled = false
	if err := s.UpdateBudget(ctx, b); err != nil {
		t.Fatal(err)
	}
	b.Enabled = true
	if err := s.UpdateBudget(ctx, b); err != nil {
		t.Fatal(err)
	}
	if !sent() {
		t.Fatal("notification was dropped although the rule did not change")
	}

	changes := map[string]func(Budget) Budget{
		"usd threshold":     func(b Budget) Budget { b.ThresholdUSD = ptrF(20); return b },
		"token threshold":   func(b Budget) Budget { b.ThresholdTokens = ptrI(5); return b },
		"period":            func(b Budget) Budget { b.Period = BudgetPeriodMonthly; return b },
		"scope":             func(b Budget) Budget { b.Scope, b.ScopeValue = BudgetScopeService, "openai"; return b },
		"threshold removed": func(b Budget) Budget { b.ThresholdUSD, b.ThresholdTokens = nil, ptrI(9); return b },
	}
	for name, change := range changes {
		mark()
		if !sent() {
			t.Fatalf("%s: setup failed", name)
		}
		b = change(b)
		if err := s.UpdateBudget(ctx, b); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if sent() {
			t.Errorf("%s: notification survived a rule change", name)
		}
	}
}
