// Package budget evaluates budget rules (daily or monthly token / USD
// thresholds) against the stored usage and remembers which breaches were
// already notified, so a period raises each alert only once.
//
// Total-only records (sources that report a combined token count and no
// input/output split) count toward token thresholds but have no cost: USD
// thresholds ignore them, since a cost cannot be computed without the split.
//
// This package only decides; delivering a notification is somebody else's job
// (call MarkNotified once it was delivered).
package budget

import (
	"context"
	"fmt"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/pricing"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// Breach is a budget whose threshold was reached in the current period and
// that has not been notified for that period yet.
type Breach struct {
	BudgetID   int64
	Scope      string // store.BudgetScope*
	ScopeValue string
	Period     string // store.BudgetPeriod*

	// PeriodKey identifies the period: "2026-11-06" (daily) or "2026-11"
	// (monthly), in the evaluator's time zone. It is the notifications_sent key.
	PeriodKey   string
	PeriodStart time.Time
	PeriodEnd   time.Time // exclusive: start of the next period

	ThresholdTokens *int64
	ThresholdUSD    *float64

	// UncostedTotalOnlyTokens is set for budgets with a USD threshold: tokens
	// of total-only records (no input/output split, no reported cost) that
	// are in the period but missing from UsedUSD because no cost can be
	// computed for them. UsedUSD is a lower bound when it is above 0.
	UncostedTotalOnlyTokens int64

	// Usage so far in the period. UsedUSD is only computed when the budget
	// has a USD threshold.
	UsedTokens int64
	UsedUSD    float64

	// Which thresholds were reached.
	TokensExceeded bool
	USDExceeded    bool

	// Estimated is true when UsedUSD includes costs computed from the price
	// table. UnknownModels lists models without a price whose usage is
	// missing from UsedUSD.
	Estimated     bool
	UnknownModels []string
	// TierUncertainModels lists models with a long-prompt price tier whose
	// records lack a request count; they were priced at the standard tier, so
	// UsedUSD may be too low.
	TierUncertainModels []string
}

// Evaluator checks budgets.
type Evaluator struct {
	st     *store.Store
	pricer *pricing.Pricer
	loc    *time.Location
}

// New returns an Evaluator. Periods are calendar days/months in loc (nil =
// the machine's local zone). pricer is used for USD thresholds.
func New(st *store.Store, pricer *pricing.Pricer, loc *time.Location) *Evaluator {
	if loc == nil {
		loc = time.Local
	}
	return &Evaluator{st: st, pricer: pricer, loc: loc}
}

// PeriodBounds returns the period containing t as [start, end) plus its key.
func PeriodBounds(period string, t time.Time, loc *time.Location) (start, end time.Time, key string, err error) {
	if loc == nil {
		loc = time.Local
	}
	l := t.In(loc)
	switch period {
	case store.BudgetPeriodDaily:
		start = time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
		end = time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
		key = start.Format("2006-01-02")
	case store.BudgetPeriodMonthly:
		start = time.Date(l.Year(), l.Month(), 1, 0, 0, 0, 0, loc)
		end = time.Date(l.Year(), l.Month()+1, 1, 0, 0, 0, 0, loc)
		key = start.Format("2006-01")
	default:
		return time.Time{}, time.Time{}, "", fmt.Errorf("budget: unknown period %q", period)
	}
	return start, end, key, nil
}

// Evaluate returns the enabled budgets that reached a threshold in the
// period containing now and were not notified for that period, ordered by
// budget id. A threshold counts as breached when usage reaches it (>=).
//
// Evaluate does not record anything: until MarkNotified is called for a
// breach, the next Evaluate returns it again.
func (e *Evaluator) Evaluate(ctx context.Context, now time.Time) ([]Breach, error) {
	return e.over(ctx, now, true)
}

// Status returns the enabled budgets that are at or over a threshold right
// now, in the period containing now, ordered by budget id. Unlike Evaluate it
// does not leave out budgets that were already notified for the period: it
// measures the usage again, so a budget whose usage fell below its threshold
// (a record was deleted, a price corrected) is no longer returned. It only
// reads; it applies the same rules as Evaluate and records nothing.
func (e *Evaluator) Status(ctx context.Context, now time.Time) ([]Breach, error) {
	return e.over(ctx, now, false)
}

// over measures every enabled budget; skipNotified leaves out the ones
// already notified for their period.
func (e *Evaluator) over(ctx context.Context, now time.Time, skipNotified bool) ([]Breach, error) {
	budgets, err := e.st.ListBudgets(ctx, true)
	if err != nil {
		return nil, err
	}
	var out []Breach
	for _, b := range budgets {
		start, end, key, err := PeriodBounds(b.Period, now, e.loc)
		if err != nil {
			return nil, err
		}
		if skipNotified {
			sent, err := e.st.NotificationSent(ctx, b.ID, key)
			if err != nil {
				return nil, err
			}
			if sent {
				continue
			}
		}
		br, breached, err := e.check(ctx, b, start, end, key, now)
		if err != nil {
			return nil, err
		}
		if breached {
			out = append(out, br)
		}
	}
	return out, nil
}

// MarkNotified records that the breach was notified, so Evaluate stops
// returning it for the rest of its period. Marking twice is harmless.
func (e *Evaluator) MarkNotified(ctx context.Context, b Breach, at time.Time) error {
	_, err := e.st.MarkNotificationSent(ctx, b.BudgetID, b.PeriodKey, at)
	return err
}

func (e *Evaluator) check(ctx context.Context, b store.Budget, start, end time.Time, key string, now time.Time) (Breach, bool, error) {
	br := Breach{
		BudgetID: b.ID, Scope: b.Scope, ScopeValue: b.ScopeValue, Period: b.Period,
		PeriodKey: key, PeriodStart: start, PeriodEnd: end,
		ThresholdTokens: b.ThresholdTokens, ThresholdUSD: b.ThresholdUSD,
	}

	// Count from the period start up to and including now (timestamps are
	// milliseconds, Filter.To is exclusive).
	f := store.Filter{From: start, To: now.Add(time.Millisecond)}
	if f.To.After(end) {
		f.To = end
	}
	// A model budget matches every spelling of the model (dated variants,
	// provider prefixes), which SQL cannot express, so it is filtered below.
	if b.Scope == store.BudgetScopeService {
		f.Services = []string{b.ScopeValue}
	}
	matches := func(modelName string) bool {
		return b.Scope != store.BudgetScopeModel ||
			pricing.CanonicalModel(modelName) == pricing.CanonicalModel(b.ScopeValue)
	}

	if b.ThresholdTokens != nil {
		if b.Scope == store.BudgetScopeModel {
			rows, err := e.st.Breakdown(ctx, f, store.DimModel)
			if err != nil {
				return Breach{}, false, err
			}
			for _, r := range rows {
				if matches(r.Key) {
					br.UsedTokens += r.TotalTokens()
				}
			}
		} else {
			t, err := e.st.Totals(ctx, f)
			if err != nil {
				return Breach{}, false, err
			}
			br.UsedTokens = t.TotalTokens()
		}
		br.TokensExceeded = br.UsedTokens >= *b.ThresholdTokens
	}

	if b.ThresholdUSD != nil {
		if e.pricer == nil {
			return Breach{}, false, fmt.Errorf("budget: budget %d has a USD threshold but no pricer is configured", b.ID)
		}
		inputs, err := e.pricer.CostInputs(ctx, e.st, f)
		if err != nil {
			return Breach{}, false, err
		}
		kept := inputs[:0]
		for _, in := range inputs {
			if matches(in.Model) {
				kept = append(kept, in)
			}
		}
		sum := e.pricer.Summarize(kept)
		br.UsedUSD, br.Estimated, br.UnknownModels = sum.USD, sum.Estimated, sum.UnknownModels
		br.TierUncertainModels = sum.TierUncertainModels
		br.UncostedTotalOnlyTokens = sum.TotalOnlyTokens
		br.USDExceeded = br.UsedUSD >= *b.ThresholdUSD
	}

	return br, br.TokensExceeded || br.USDExceeded, nil
}
