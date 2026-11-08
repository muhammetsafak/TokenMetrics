package budget

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

var ctx = context.Background()

func setup(t *testing.T, loc *time.Location) (*store.Store, *Evaluator) {
	t.Helper()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "b.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	p, err := pricing.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return st, New(st, p, loc)
}

func put(t *testing.T, st *store.Store, id string, ts time.Time, service, modelName string, in, out int64) {
	t.Helper()
	_, _, err := st.InsertBatch(ctx, []model.UsageRecord{{
		Source: model.SourceClaudeCode, ExternalID: id, Timestamp: ts,
		Service: service, Model: modelName, InputTokens: in, OutputTokens: out, RequestCount: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
}

func addBudget(t *testing.T, st *store.Store, b store.Budget) int64 {
	t.Helper()
	b.Enabled = true
	id, err := st.CreateBudget(ctx, b)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func i64(v int64) *int64     { return &v }
func f64(v float64) *float64 { return &v }

var now = time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)

func TestDailyBreachReturnedOnceUntilNextPeriod(t *testing.T) {
	st, ev := setup(t, time.UTC)
	id := addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1000)})
	put(t, st, "a", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 600, 500) // 1100 today

	got, err := ev.Evaluate(ctx, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("Evaluate = %+v, %v; want one breach", got, err)
	}
	b := got[0]
	if b.BudgetID != id || b.PeriodKey != "2026-11-06" || b.UsedTokens != 1100 || !b.TokensExceeded || b.USDExceeded {
		t.Fatalf("breach = %+v", b)
	}
	if !b.PeriodStart.Equal(time.Date(2026, 11, 6, 0, 0, 0, 0, time.UTC)) || !b.PeriodEnd.Equal(time.Date(2026, 11, 7, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("period = %v..%v", b.PeriodStart, b.PeriodEnd)
	}

	// Not marked yet: still reported (delivery may have failed).
	if got, _ := ev.Evaluate(ctx, now.Add(time.Minute)); len(got) != 1 {
		t.Fatalf("before MarkNotified: %d breaches, want 1", len(got))
	}

	if err := ev.MarkNotified(ctx, b, now); err != nil {
		t.Fatal(err)
	}
	if got, err := ev.Evaluate(ctx, now.Add(2*time.Minute)); err != nil || len(got) != 0 {
		t.Fatalf("after MarkNotified: %+v, %v; want none", got, err)
	}
	// More usage in the same period does not re-trigger it.
	put(t, st, "b", now.Add(time.Minute), "anthropic", "claude-sonnet-5-5", 5000, 0)
	if got, _ := ev.Evaluate(ctx, now.Add(3*time.Minute)); len(got) != 0 {
		t.Fatalf("same period, more usage: %+v, want none", got)
	}
	// Marking twice is harmless.
	if err := ev.MarkNotified(ctx, b, now); err != nil {
		t.Fatalf("second MarkNotified: %v", err)
	}

	// Next day: fresh period, usage restarts from zero.
	tomorrow := now.Add(24 * time.Hour)
	if got, _ := ev.Evaluate(ctx, tomorrow); len(got) != 0 {
		t.Fatalf("next day without usage: %+v", got)
	}
	put(t, st, "c", tomorrow.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 2000, 0)
	got, _ = ev.Evaluate(ctx, tomorrow)
	if len(got) != 1 || got[0].PeriodKey != "2026-11-07" || got[0].UsedTokens != 2000 {
		t.Fatalf("next day: %+v, want one breach for 2026-11-07 with 2000 tokens", got)
	}
}

func TestMonthlyBreachOncePerMonth(t *testing.T) {
	st, ev := setup(t, time.UTC)
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodMonthly, ThresholdTokens: i64(1000)})
	put(t, st, "a", time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "anthropic", "claude-sonnet-5-5", 700, 0)
	put(t, st, "b", time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC), "anthropic", "claude-sonnet-5-5", 400, 0)
	// October usage must not count for November.
	put(t, st, "oct", time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC), "anthropic", "claude-sonnet-5-5", 9999, 0)

	got, err := ev.Evaluate(ctx, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].PeriodKey != "2026-11" || got[0].UsedTokens != 1100 {
		t.Fatalf("breach = %+v, want key 2026-11 with 1100 tokens", got[0])
	}
	if !got[0].PeriodEnd.Equal(time.Date(2026, 12, 1, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("PeriodEnd = %v", got[0].PeriodEnd)
	}
	if err := ev.MarkNotified(ctx, got[0], now); err != nil {
		t.Fatal(err)
	}
	// Later in the same month, and on the last day of it: still silent.
	for _, at := range []time.Time{now.Add(10 * 24 * time.Hour), time.Date(2026, 11, 30, 23, 59, 0, 0, time.UTC)} {
		if got, _ := ev.Evaluate(ctx, at); len(got) != 0 {
			t.Fatalf("at %v: %+v, want none", at, got)
		}
	}
	// December starts clean.
	dec := time.Date(2026, 12, 2, 0, 0, 0, 0, time.UTC)
	put(t, st, "dec", dec.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 1500, 0)
	got, _ = ev.Evaluate(ctx, dec)
	if len(got) != 1 || got[0].PeriodKey != "2026-12" {
		t.Fatalf("december: %+v", got)
	}
}

func TestThresholdBoundary(t *testing.T) {
	st, ev := setup(t, time.UTC)
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1000)})
	put(t, st, "a", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 999, 0)
	if got, _ := ev.Evaluate(ctx, now); len(got) != 0 {
		t.Fatalf("999 of 1000: %+v, want none", got)
	}
	put(t, st, "b", now.Add(-time.Minute), "anthropic", "claude-sonnet-5-5", 1, 0)
	if got, _ := ev.Evaluate(ctx, now); len(got) != 1 {
		t.Fatalf("exactly 1000 of 1000: %+v, want a breach", got)
	}
}

func TestUSDThresholdAndBothThresholds(t *testing.T) {
	st, ev := setup(t, time.UTC)
	// sonnet-5-5: 1M input = $2.00, 100k output = $1.00 -> $3.00 total.
	put(t, st, "a", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 1_000_000, 100_000)

	usdOnly := addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: f64(3.00)})
	notYet := addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: f64(3.01)})
	both := addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily,
		ThresholdTokens: i64(10_000_000), ThresholdUSD: f64(1)}) // only USD reached

	got, err := ev.Evaluate(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]Breach{}
	for _, b := range got {
		byID[b.BudgetID] = b
	}
	if _, ok := byID[notYet]; ok || len(got) != 2 {
		t.Fatalf("breaches = %+v; $3.00 of $3.01 must not breach", got)
	}
	b := byID[usdOnly]
	if math.Abs(b.UsedUSD-3.0) > 1e-9 || !b.USDExceeded || b.TokensExceeded || !b.Estimated || b.UsedTokens != 0 {
		t.Fatalf("usd-only breach = %+v", b)
	}
	c := byID[both]
	if !c.USDExceeded || c.TokensExceeded {
		t.Fatalf("both-threshold breach = %+v", c)
	}
	if got[0].BudgetID > got[1].BudgetID {
		t.Fatal("breaches are not ordered by budget id")
	}
}

func TestUSDUsesReportedCostAndFlagsUnknownModels(t *testing.T) {
	st, ev := setup(t, time.UTC)
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: f64(5)})
	cost := 6.0
	_, _, err := st.InsertBatch(ctx, []model.UsageRecord{
		{Source: model.SourceManual, ExternalID: "r", Timestamp: now.Add(-time.Hour), Model: "mystery-model", ReportedCostUSD: &cost},
		{Source: model.SourceManual, ExternalID: "u", Timestamp: now.Add(-time.Hour), Model: "other-mystery", InputTokens: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ev.Evaluate(ctx, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if got[0].UsedUSD != 6 || got[0].Estimated {
		t.Fatalf("breach = %+v, want exactly the reported 6 USD", got[0])
	}
	if len(got[0].UnknownModels) != 1 || got[0].UnknownModels[0] != "other-mystery" {
		t.Fatalf("UnknownModels = %v", got[0].UnknownModels)
	}
}

func TestScopes(t *testing.T) {
	st, ev := setup(t, time.UTC)
	ts := now.Add(-time.Hour)
	put(t, st, "a", ts, "anthropic", "claude-sonnet-5-5-20260101", 600, 0) // dated spelling
	put(t, st, "b", ts, "anthropic", "claude-sonnet-5-5", 500, 0)
	put(t, st, "c", ts, "anthropic", "claude-opus-5-5", 50_000, 0)
	put(t, st, "d", ts, "openai", "gpt-5", 70_000, 0)

	svc := addBudget(t, st, store.Budget{Scope: store.BudgetScopeService, ScopeValue: "openai", Period: store.BudgetPeriodDaily, ThresholdTokens: i64(60_000)})
	mdl := addBudget(t, st, store.Budget{Scope: store.BudgetScopeModel, ScopeValue: "claude-sonnet-5-5", Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1100)})
	mdlUSD := addBudget(t, st, store.Budget{Scope: store.BudgetScopeModel, ScopeValue: "claude-sonnet-5-5", Period: store.BudgetPeriodDaily, ThresholdUSD: f64(1)})
	skip := addBudget(t, st, store.Budget{Scope: store.BudgetScopeService, ScopeValue: "google", Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1)})

	got, err := ev.Evaluate(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[int64]Breach{}
	for _, b := range got {
		byID[b.BudgetID] = b
	}
	if _, ok := byID[skip]; ok {
		t.Error("service budget with no usage must not breach")
	}
	if b := byID[svc]; b.UsedTokens != 70_000 {
		t.Errorf("service budget used %d, want 70000", b.UsedTokens)
	}
	if b := byID[mdl]; b.UsedTokens != 1100 { // dated + plain spelling, not opus or gpt
		t.Errorf("model budget used %d, want 1100", b.UsedTokens)
	}
	if _, ok := byID[mdlUSD]; ok { // 1100 input tokens of sonnet = $0.0022
		t.Errorf("model USD budget breached: %+v", byID[mdlUSD])
	}
}

func TestDisabledBudgetIgnored(t *testing.T) {
	st, ev := setup(t, time.UTC)
	id, err := st.CreateBudget(ctx, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1), Enabled: false})
	if err != nil {
		t.Fatal(err)
	}
	put(t, st, "a", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 100, 0)
	if got, _ := ev.Evaluate(ctx, now); len(got) != 0 {
		t.Fatalf("disabled budget breached: %+v", got)
	}
	b := store.Budget{ID: id, Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1), Enabled: true}
	if err := st.UpdateBudget(ctx, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := ev.Evaluate(ctx, now); len(got) != 1 {
		t.Fatalf("enabled budget: %+v", got)
	}
}

func TestPeriodsFollowTimeZone(t *testing.T) {
	istanbul, err := time.LoadLocation("Europe/Istanbul") // UTC+3, no DST
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	st, ev := setup(t, istanbul)
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1)})
	at := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC) // 13:00 local
	// 20:59 UTC on the 5th is 23:59 local on the 5th: yesterday.
	put(t, st, "yesterday", time.Date(2026, 11, 5, 20, 59, 0, 0, time.UTC), "anthropic", "claude-sonnet-5-5", 5, 0)
	if got, _ := ev.Evaluate(ctx, at); len(got) != 0 {
		t.Fatalf("local yesterday counted today: %+v", got)
	}
	// 21:00 UTC on the 5th is 00:00 local on the 6th: today.
	put(t, st, "midnight", time.Date(2026, 11, 5, 21, 0, 0, 0, time.UTC), "anthropic", "claude-sonnet-5-5", 7, 0)
	got, _ := ev.Evaluate(ctx, at)
	if len(got) != 1 || got[0].PeriodKey != "2026-11-06" || got[0].UsedTokens != 7 {
		t.Fatalf("got %+v, want key 2026-11-06 with 7 tokens", got)
	}
}

func TestPeriodBounds(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	// 2026-11-01 is the 25-hour day when US daylight saving time ends.
	start, end, key, err := PeriodBounds(store.BudgetPeriodDaily, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC), ny)
	if err != nil || key != "2026-11-01" || end.Sub(start) != 25*time.Hour {
		t.Fatalf("daily: %v..%v key=%s err=%v", start, end, key, err)
	}
	start, end, key, err = PeriodBounds(store.BudgetPeriodMonthly, time.Date(2026, 12, 31, 23, 0, 0, 0, time.UTC), time.UTC)
	if err != nil || key != "2026-12" || !end.Equal(time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)) || start.Day() != 1 {
		t.Fatalf("monthly: %v..%v key=%s err=%v", start, end, key, err)
	}
	if _, _, _, err := PeriodBounds("weekly", now, time.UTC); err == nil {
		t.Fatal("unknown period: want error")
	}
}

func TestNoBudgets(t *testing.T) {
	_, ev := setup(t, time.UTC)
	if got, err := ev.Evaluate(ctx, now); err != nil || len(got) != 0 {
		t.Fatalf("got %+v, %v", got, err)
	}
}

func TestRaisingThresholdAllowsANewAlertInTheSamePeriod(t *testing.T) {
	st, ev := setup(t, time.UTC)
	id := addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1000)})
	put(t, st, "a", now.Add(-time.Hour), "anthropic", "claude-sonnet-5-5", 1500, 0)

	got, _ := ev.Evaluate(ctx, now)
	if len(got) != 1 {
		t.Fatalf("first alert: %+v", got)
	}
	if err := ev.MarkNotified(ctx, got[0], now); err != nil {
		t.Fatal(err)
	}

	// The user raises the threshold; usage later passes the new one.
	if err := st.UpdateBudget(ctx, store.Budget{ID: id, Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(2000), Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if got, _ := ev.Evaluate(ctx, now); len(got) != 0 {
		t.Fatalf("1500 of the new 2000 must not alert: %+v", got)
	}
	put(t, st, "b", now.Add(time.Minute), "anthropic", "claude-sonnet-5-5", 600, 0)
	if got, _ := ev.Evaluate(ctx, now.Add(2*time.Minute)); len(got) != 1 || got[0].UsedTokens != 2100 {
		t.Fatalf("new threshold must alert again in the same period: %+v", got)
	}
}

func TestUnknownRequestCountDoesNotInflateUSDAndIsFlagged(t *testing.T) {
	st, ev := setup(t, time.UTC)
	// Daily API bucket (request_count 0 = unknown): priced at the standard tier,
	// 1M * 0.10 = 0.10 USD, and flagged.
	if _, _, err := st.InsertBatch(ctx, []model.UsageRecord{{
		Source: model.SourceAnthropicAPI, ExternalID: "bucket", Timestamp: now.Add(-time.Hour),
		Service: "anthropic", Model: "claude-haiku-5-5", InputTokens: 1_000_000, // RequestCount 0
	}}); err != nil {
		t.Fatal(err)
	}
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: f64(0.10)})
	got, err := ev.Evaluate(ctx, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("got %+v, %v", got, err)
	}
	if math.Abs(got[0].UsedUSD-0.10) > 1e-9 {
		t.Fatalf("UsedUSD = %v, want 0.10", got[0].UsedUSD)
	}
	if len(got[0].TierUncertainModels) != 1 || got[0].TierUncertainModels[0] != "claude-haiku-5-5" {
		t.Fatalf("TierUncertainModels = %v", got[0].TierUncertainModels)
	}
}

func TestHaikuLongPromptRecordsRaiseUSDUsage(t *testing.T) {
	st, ev := setup(t, time.UTC)
	// 200K prompt on claude-haiku-5-5 is billed at the long-prompt tier:
	// 0.2 M * 0.50 = 0.10 USD (the standard rate would give 0.02).
	put(t, st, "long", now.Add(-time.Hour), "anthropic", "claude-haiku-5-5", 200_000, 0)
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: f64(0.10)})
	got, err := ev.Evaluate(ctx, now)
	if err != nil || len(got) != 1 || math.Abs(got[0].UsedUSD-0.10) > 1e-9 {
		t.Fatalf("got %+v, %v; want a breach at 0.10 USD", got, err)
	}
}

func TestTotalOnlyTokensCountButNeverCost(t *testing.T) {
	st, ev := setup(t, time.UTC)
	_, _, err := st.InsertBatch(ctx, []model.UsageRecord{{
		Source: model.SourceCodexCLI, ExternalID: "t1", Timestamp: now.Add(-time.Hour),
		Service: "openai", Model: "gpt-5.5", RequestCount: 1, TotalOnly: true, TotalOnlyTokens: 5_000_000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1_000_000)})
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeModel, ScopeValue: "gpt-5.5", Period: store.BudgetPeriodDaily, ThresholdTokens: i64(4_000_000)})
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdUSD: f64(0.01)})

	got, err := ev.Evaluate(ctx, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("breaches = %+v; want the two token budgets only (no cost to compare)", got)
	}
	for _, b := range got {
		if b.ThresholdUSD != nil || b.UsedTokens != 5_000_000 {
			t.Errorf("breach = %+v; want a token breach of 5,000,000 tokens", b)
		}
	}
}

func TestUSDBreachReportsUncostedTotalOnlyTokens(t *testing.T) {
	st, ev := setup(t, time.UTC)
	put(t, st, "split", now.Add(-2*time.Hour), "openai", "gpt-5.5", 1_000_000, 0) // priced
	_, _, err := st.InsertBatch(ctx, []model.UsageRecord{{
		Source: model.SourceCodexCLI, ExternalID: "t1", Timestamp: now.Add(-time.Hour),
		Service: "openai", Model: "gpt-5.5", RequestCount: 1, TotalOnly: true, TotalOnlyTokens: 7_000_000,
	}})
	if err != nil {
		t.Fatal(err)
	}
	addBudget(t, st, store.Budget{Scope: store.BudgetScopeService, ScopeValue: "openai", Period: store.BudgetPeriodDaily, ThresholdUSD: f64(0.01)})
	got, err := ev.Evaluate(ctx, now)
	if err != nil || len(got) != 1 {
		t.Fatalf("Evaluate = %+v, %v; want one breach", got, err)
	}
	if got[0].UncostedTotalOnlyTokens != 7_000_000 || got[0].UsedUSD <= 0 || len(got[0].UnknownModels) != 0 {
		t.Fatalf("breach = %+v; want 7,000,000 uncosted total-only tokens flagged", got[0])
	}
}

func TestStatusIgnoresNotificationsAndFollowsUsage(t *testing.T) {
	st, ev := setup(t, time.UTC)
	id := addBudget(t, st, store.Budget{Scope: store.BudgetScopeTotal, Period: store.BudgetPeriodDaily, ThresholdTokens: i64(1000)})

	if got, err := ev.Status(ctx, now); err != nil || len(got) != 0 {
		t.Fatalf("no usage: Status = %+v, %v", got, err)
	}
	// A manual record, so that it can be deleted again below.
	if _, _, err := st.InsertBatch(ctx, []model.UsageRecord{{
		Source: model.SourceManual, ExternalID: "m1", Timestamp: now.Add(-time.Hour),
		Service: "anthropic", Model: "claude-sonnet-5-5", InputTokens: 600, OutputTokens: 500, RequestCount: 1,
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := ev.Status(ctx, now)
	if err != nil || len(got) != 1 || got[0].BudgetID != id || got[0].UsedTokens != 1100 {
		t.Fatalf("over: Status = %+v, %v", got, err)
	}
	// Status records nothing: Evaluate still reports it.
	if ev2, _ := ev.Evaluate(ctx, now); len(ev2) != 1 {
		t.Fatalf("Evaluate after Status = %d breaches, want 1", len(ev2))
	}

	// Notified: Evaluate leaves it out, Status still reports it.
	if err := ev.MarkNotified(ctx, got[0], now); err != nil {
		t.Fatal(err)
	}
	if ev2, _ := ev.Evaluate(ctx, now); len(ev2) != 0 {
		t.Fatalf("Evaluate after MarkNotified = %d, want 0", len(ev2))
	}
	if got, _ := ev.Status(ctx, now); len(got) != 1 {
		t.Fatalf("Status after MarkNotified = %d, want 1", len(got))
	}

	// Usage falls below the threshold (the record was deleted): Status clears.
	if found, err := st.DeleteManualRecord(ctx, "m1"); err != nil || !found {
		t.Fatalf("DeleteManualRecord = %v, %v", found, err)
	}
	if got, err := ev.Status(ctx, now); err != nil || len(got) != 0 {
		t.Fatalf("usage fell: Status = %+v, %v", got, err)
	}
}
