// Package limits computes how much of an (estimated) subscription limit
// window has been used: a rolling 5-hour window and a weekly window.
//
// The limits are entered by the user and the vendors do not publish exact
// accounting rules, so every result is an estimate and says so
// (Result.Estimated is always true).
package limits

import (
	"context"
	"fmt"
	"time"
	_ "time/tzdata" // IANA zone names must resolve on Windows too

	"github.com/muhammetsafak/TokenMetrics/internal/pricing"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

// SettingWeeklyStart is the settings key holding the WeeklyStart JSON object.
const SettingWeeklyStart = "limits.weekly_start"

// FiveHourSpan is the length of the rolling short window.
const FiveHourSpan = 5 * time.Hour

// WeeklyStart says when the weekly window begins.
type WeeklyStart struct {
	Weekday  int    `json:"weekday"`  // 0 = Sunday ... 6 = Saturday (time.Weekday)
	Hour     int    `json:"hour"`     // 0-23, local to Timezone
	Timezone string `json:"timezone"` // IANA name; "" = the machine's local zone
}

// DefaultWeeklyStart is used when the setting is absent: Monday 00:00 local.
func DefaultWeeklyStart() WeeklyStart {
	return WeeklyStart{Weekday: int(time.Monday), Hour: 0}
}

// Validate checks the field ranges and that the time zone exists.
func (w WeeklyStart) Validate() error {
	if w.Weekday < 0 || w.Weekday > 6 {
		return fmt.Errorf("limits: weekday %d out of range 0-6", w.Weekday)
	}
	if w.Hour < 0 || w.Hour > 23 {
		return fmt.Errorf("limits: hour %d out of range 0-23", w.Hour)
	}
	_, err := w.Location()
	return err
}

// Location resolves the time zone.
func (w WeeklyStart) Location() (*time.Location, error) {
	if w.Timezone == "" {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(w.Timezone)
	if err != nil {
		return nil, fmt.Errorf("limits: time zone %q: %w", w.Timezone, err)
	}
	return loc, nil
}

// FiveHourWindow returns the rolling window ending at now: [now-5h, now].
func FiveHourWindow(now time.Time) (start, end time.Time) {
	return now.Add(-FiveHourSpan), now
}

// WeeklyWindow returns the weekly window containing now: it starts at the
// most recent occurrence of cfg's weekday and hour (in cfg's zone) that is not
// after now, and ends one calendar week later (the reset time). Using
// calendar arithmetic keeps the start at the same local hour across a DST
// change, so the window may be 167 or 169 hours long.
func WeeklyWindow(now time.Time, cfg WeeklyStart) (start, end time.Time, err error) {
	loc, err := cfg.Location()
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	local := now.In(loc)
	back := (int(local.Weekday()) - cfg.Weekday + 7) % 7
	start = time.Date(local.Year(), local.Month(), local.Day()-back, cfg.Hour, 0, 0, 0, loc)
	if start.After(now) {
		start = time.Date(local.Year(), local.Month(), local.Day()-back-7, cfg.Hour, 0, 0, 0, loc)
	}
	end = time.Date(start.Year(), start.Month(), start.Day()+7, cfg.Hour, 0, 0, 0, loc)
	return start, end, nil
}

// Result is the consumption of one limit window.
type Result struct {
	Service string `json:"service"` // "" = all services
	Kind    string `json:"kind"`    // "5h" or "weekly"
	Metric  string `json:"metric"`  // store.Metric*

	// Used and Limit are in the unit of Metric: tokens, or USD for cost_usd.
	Used  float64 `json:"used"`
	Limit float64 `json:"limit"`
	// Ratio is Used/Limit; it can exceed 1 when the limit is passed.
	Ratio float64 `json:"ratio"`

	// WindowStart is when usage counting starts. WindowEnd is now for the
	// rolling 5h window and the reset time for the weekly window.
	WindowStart time.Time `json:"window_start"`
	WindowEnd   time.Time `json:"window_end"`

	// Estimated is always true: limits and prices are approximations.
	Estimated bool `json:"estimated"`

	// UnknownModels is set for the cost_usd metric: models used in the window
	// that have no price, whose usage is therefore missing from Used.
	UnknownModels []string `json:"unknown_models,omitempty"`

	// MissingTotalOnlyTokens counts the tokens of total-only records (no
	// input/output split) in the window that Used leaves out: all of them for
	// the output_tokens metric, those without a reported cost for cost_usd.
	// Used is a lower bound when it is above 0.
	MissingTotalOnlyTokens int64 `json:"missing_total_only_tokens,omitempty"`

	// TierUncertainModels is set for the cost_usd metric: models with a
	// long-prompt price tier whose records lack a request count, so the tier
	// could not be determined and Used may be too low for them.
	TierUncertainModels []string `json:"tier_uncertain_models,omitempty"`
}

// Calculator evaluates limit windows against the stored usage.
type Calculator struct {
	st     *store.Store
	pricer *pricing.Pricer
}

// New returns a Calculator. pricer is only used for the cost_usd metric.
func New(st *store.Store, pricer *pricing.Pricer) *Calculator {
	return &Calculator{st: st, pricer: pricer}
}

// WeeklyStartSetting loads the weekly start from settings, falling back to
// DefaultWeeklyStart when the setting is absent.
func (c *Calculator) WeeklyStartSetting(ctx context.Context) (WeeklyStart, error) {
	cfg := DefaultWeeklyStart()
	var stored WeeklyStart
	found, err := c.st.GetSetting(ctx, SettingWeeklyStart, &stored)
	if err != nil {
		return WeeklyStart{}, err
	}
	if found {
		cfg = stored
	}
	if err := cfg.Validate(); err != nil {
		return WeeklyStart{}, err
	}
	return cfg, nil
}

// EvaluateAll evaluates every stored limit window at now, in the store's order.
func (c *Calculator) EvaluateAll(ctx context.Context, now time.Time) ([]Result, error) {
	windows, err := c.st.ListLimitWindows(ctx)
	if err != nil {
		return nil, err
	}
	var cfg *WeeklyStart
	out := make([]Result, 0, len(windows))
	for _, lw := range windows {
		if lw.Kind == store.LimitKindWeekly && cfg == nil {
			w, err := c.WeeklyStartSetting(ctx)
			if err != nil {
				return nil, err
			}
			cfg = &w
		}
		r, err := c.evaluate(ctx, lw, now, cfg)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

// Evaluate evaluates one limit window at now.
func (c *Calculator) Evaluate(ctx context.Context, lw store.LimitWindow, now time.Time) (Result, error) {
	var cfg *WeeklyStart
	if lw.Kind == store.LimitKindWeekly {
		w, err := c.WeeklyStartSetting(ctx)
		if err != nil {
			return Result{}, err
		}
		cfg = &w
	}
	return c.evaluate(ctx, lw, now, cfg)
}

func (c *Calculator) evaluate(ctx context.Context, lw store.LimitWindow, now time.Time, cfg *WeeklyStart) (Result, error) {
	var start, end time.Time
	switch lw.Kind {
	case store.LimitKind5h:
		start, end = FiveHourWindow(now)
	case store.LimitKindWeekly:
		var err error
		if start, end, err = WeeklyWindow(now, *cfg); err != nil {
			return Result{}, err
		}
	default:
		return Result{}, fmt.Errorf("limits: unknown window kind %q", lw.Kind)
	}
	if lw.Limit <= 0 {
		return Result{}, fmt.Errorf("limits: window %d has a non-positive limit", lw.ID)
	}

	// Usage counts from start (inclusive) up to and including now. Timestamps
	// are stored in milliseconds and Filter.To is exclusive, hence +1ms.
	f := store.Filter{From: start, To: now.Add(time.Millisecond)}
	if lw.Service != "" {
		f.Services = []string{lw.Service}
	}

	res := Result{
		Service:     lw.Service,
		Kind:        lw.Kind,
		Metric:      lw.Metric,
		Limit:       float64(lw.Limit),
		WindowStart: start,
		WindowEnd:   end,
		Estimated:   true,
	}
	switch lw.Metric {
	case store.MetricTotalTokens, store.MetricOutputTokens:
		t, err := c.st.Totals(ctx, f)
		if err != nil {
			return Result{}, err
		}
		if lw.Metric == store.MetricTotalTokens {
			res.Used = float64(t.TotalTokens())
		} else {
			res.Used = float64(t.OutputTokens)
			res.MissingTotalOnlyTokens = t.TotalOnlyTokens
		}
	case store.MetricCostUSD:
		if c.pricer == nil {
			return Result{}, fmt.Errorf("limits: metric %q needs a pricer", lw.Metric)
		}
		inputs, err := c.pricer.CostInputs(ctx, c.st, f)
		if err != nil {
			return Result{}, err
		}
		sum := c.pricer.Summarize(inputs)
		res.Used = sum.USD
		res.UnknownModels = sum.UnknownModels
		res.TierUncertainModels = sum.TierUncertainModels
		res.MissingTotalOnlyTokens = sum.TotalOnlyTokens
		res.Limit = float64(lw.Limit) / 100 // stored in US cents
	default:
		return Result{}, fmt.Errorf("limits: unknown metric %q", lw.Metric)
	}
	res.Ratio = res.Used / res.Limit
	return res, nil
}
