// Package pricing estimates the USD cost of token usage from a built-in price
// table (defaults.json) plus user overrides stored in the database.
//
// Everything computed here is an estimate: prices change, subscriptions are
// not billed per token, and a source that reports its own cost always wins.
package pricing

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

//go:embed defaults.json
var defaultsJSON []byte

// Price is a model's price in USD per one million tokens.
type Price struct {
	Input        float64 `json:"input"`
	Output       float64 `json:"output"`
	CacheRead    float64 `json:"cache_read"`
	CacheWrite5m float64 `json:"cache_write_5m"`
	CacheWrite1h float64 `json:"cache_write_1h"`
}

// Entry is one row of the built-in price table.
type Entry struct {
	Price
	Service string `json:"service"`
	Source  string `json:"source"` // where the price was taken from

	// LongPrompt, when set, replaces Price for requests whose prompt (input +
	// cache read + cache writes) is over LongPromptThreshold tokens.
	LongPrompt *Price `json:"long_prompt,omitempty"`

	// ValidUntil ("2006-01-02", inclusive) marks a price that stops applying
	// after that day: a promotion that ends or a model that is re-routed.
	// Empty means no known end.
	ValidUntil string `json:"valid_until,omitempty"`
}

// LongPromptThreshold is the prompt size, in tokens, above which an entry's
// LongPrompt price applies.
const LongPromptThreshold = 100_000

// table is the decoded defaults.json.
type table struct {
	AsOf   string           `json:"as_of"`
	Note   string           `json:"note"`
	Models map[string]Entry `json:"models"`
}

var loadDefaults = sync.OnceValues(func() (table, error) {
	var t table
	if err := json.Unmarshal(defaultsJSON, &t); err != nil {
		return table{}, fmt.Errorf("pricing: decode defaults.json: %w", err)
	}
	if len(t.Models) == 0 {
		return table{}, fmt.Errorf("pricing: defaults.json has no models")
	}
	return t, nil
})

// dateSuffix matches the version suffixes providers append to a model id:
// -20260101, @20260101, -2026-01-01 and -latest.
var dateSuffix = regexp.MustCompile(`(?:[-@]\d{8}|-\d{4}-\d{2}-\d{2}|-latest)$`)

// modelKeys returns the lookup keys for a model name, most specific first:
// the normalized name, then the same name without a date suffix. Normalizing
// lower-cases, drops a "provider/" or "models/" path prefix and a trailing
// "[1m]"-style context tag.
//
// Matching is deliberately not a plain prefix match: "claude-opus-5-5" must
// never fall back to "claude-opus-5", so only a date/latest suffix is ignored.
func modelKeys(name string) []string {
	s := strings.ToLower(strings.TrimSpace(name))
	if i := strings.LastIndex(s, "/"); i >= 0 {
		s = s[i+1:]
	}
	if strings.HasSuffix(s, "]") {
		if i := strings.LastIndex(s, "["); i > 0 {
			s = s[:i]
		}
	}
	if s == "" {
		return nil
	}
	keys := []string{s}
	if stripped := dateSuffix.ReplaceAllString(s, ""); stripped != s && stripped != "" {
		keys = append(keys, stripped)
	}
	return keys
}

// CanonicalModel returns the form of a model name used for matching: lower
// case, without a provider path prefix, context tag or date/latest suffix.
// "anthropic/Claude-Sonnet-5-5-20260101" and "claude-sonnet-5-5" are equal.
func CanonicalModel(name string) string {
	keys := modelKeys(name)
	if len(keys) == 0 {
		return ""
	}
	return keys[len(keys)-1]
}

// Pricer prices usage. It is immutable after construction and safe for
// concurrent use.
type Pricer struct {
	asOf      string
	defaults  map[string]Entry
	overrides map[string]store.PriceOverride
}

// New builds a Pricer from the built-in table and the given overrides.
func New(overrides []store.PriceOverride) (*Pricer, error) {
	t, err := loadDefaults()
	if err != nil {
		return nil, err
	}
	p := &Pricer{
		asOf:      t.AsOf,
		defaults:  make(map[string]Entry, len(t.Models)),
		overrides: make(map[string]store.PriceOverride, len(overrides)),
	}
	for name, e := range t.Models {
		keys := modelKeys(name)
		if len(keys) == 0 {
			continue
		}
		p.defaults[keys[0]] = e
	}
	for _, o := range overrides {
		keys := modelKeys(o.Model)
		if len(keys) == 0 {
			continue
		}
		p.overrides[keys[0]] = o
	}
	return p, nil
}

// Load builds a Pricer from the built-in table and the overrides in st.
func Load(ctx context.Context, st *store.Store) (*Pricer, error) {
	overrides, err := st.ListPriceOverrides(ctx)
	if err != nil {
		return nil, err
	}
	return New(overrides)
}

// AsOf returns the date of the built-in price table.
func (p *Pricer) AsOf() string { return p.asOf }

// Resolved is the price found for a model.
type Resolved struct {
	Price
	// Long is the price for prompts over LongPromptThreshold; nil when the
	// model has a single price. A user override replaces its components just
	// like the standard ones; components that are not overridden keep the
	// tier's own price.
	Long       *Price
	ValidUntil string // see Entry.ValidUntil
	Service    string
	Source     string // "" when the price comes only from an override
	Overridden bool   // at least one component comes from a user override
}

// Lookup finds the price for a model name. It tries the exact name, then the
// name without a date suffix, in both the overrides and the defaults; an
// override component replaces the matching default component. A model that
// has no default is known only when its override sets both the input and the
// output price (a missing component would silently be priced at 0).
func (p *Pricer) Lookup(name string) (Resolved, bool) {
	var res Resolved
	var def *Entry
	var ov *store.PriceOverride
	for _, k := range modelKeys(name) {
		if def == nil {
			if e, ok := p.defaults[k]; ok {
				def = &e
			}
		}
		if ov == nil {
			if o, ok := p.overrides[k]; ok {
				ov = &o
			}
		}
		if def != nil && ov != nil {
			break
		}
	}
	if def != nil {
		res.Price, res.Service, res.Source = def.Price, def.Service, def.Source
		res.Long, res.ValidUntil = def.LongPrompt, def.ValidUntil
	}
	if ov != nil {
		set := func(dst *float64, v *float64) bool {
			if v == nil {
				return false
			}
			*dst = *v
			return true
		}
		a := set(&res.Input, ov.InputPerMTok)
		b := set(&res.Output, ov.OutputPerMTok)
		c := set(&res.CacheRead, ov.CacheReadPerMTok)
		d := set(&res.CacheWrite5m, ov.CacheWrite5mPerMTok)
		e := set(&res.CacheWrite1h, ov.CacheWrite1hPerMTok)
		res.Overridden = a || b || c || d || e
		if res.Long != nil {
			// Apply the same overrides to the long-prompt tier (on a copy: the
			// table entry is shared).
			long := *res.Long
			set(&long.Input, ov.InputPerMTok)
			set(&long.Output, ov.OutputPerMTok)
			set(&long.CacheRead, ov.CacheReadPerMTok)
			set(&long.CacheWrite5m, ov.CacheWrite5mPerMTok)
			set(&long.CacheWrite1h, ov.CacheWrite1hPerMTok)
			res.Long = &long
		}
		if def == nil && (ov.InputPerMTok == nil || ov.OutputPerMTok == nil) {
			return Resolved{}, false
		}
	}
	if def == nil && ov == nil {
		return Resolved{}, false
	}
	return res, true
}

// ModelEntry is a built-in price with its model name.
type ModelEntry struct {
	Model string
	Entry
}

// ListDefaults returns the built-in table ordered by service, then model.
func (p *Pricer) ListDefaults() []ModelEntry {
	out := make([]ModelEntry, 0, len(p.defaults))
	for name, e := range p.defaults {
		out = append(out, ModelEntry{Model: name, Entry: e})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Service != out[j].Service {
			return out[i].Service < out[j].Service
		}
		return out[i].Model < out[j].Model
	})
	return out
}

// Expired reports whether the price stopped applying before day (a time in
// any zone; only its UTC calendar date is compared). An override never expires.
func (r Resolved) Expired(day time.Time) bool {
	if r.ValidUntil == "" || r.Overridden {
		return false
	}
	until, err := time.Parse("2006-01-02", r.ValidUntil)
	if err != nil {
		return false
	}
	d := day.UTC()
	return time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, time.UTC).After(until)
}

// CostInputs loads the per-model cost inputs for f from st, using this
// package's long-prompt threshold. Pass the result to Summarize.
func (p *Pricer) CostInputs(ctx context.Context, st *store.Store, f store.Filter) ([]store.CostInput, error) {
	return st.CostInputs(ctx, f, LongPromptThreshold)
}

const perMillion = 1_000_000.0

func priceTokens(pr Price, input, output, cacheRead, cacheWrite5m, cacheWrite1h int64) float64 {
	return (float64(input)*pr.Input +
		float64(output)*pr.Output +
		float64(cacheRead)*pr.CacheRead +
		float64(cacheWrite5m)*pr.CacheWrite5m +
		float64(cacheWrite1h)*pr.CacheWrite1h) / perMillion
}

// Cost returns the cost of one record in USD.
//
//   - A record whose source reported a cost returns that cost with
//     estimated=false.
//   - Otherwise the cost is computed from the price table with
//     estimated=true. Thinking tokens are not added: providers count them
//     inside the output tokens.
//   - A model with a long-prompt tier (Claude Haiku 5.5) is priced at that tier
//     when the record's prompt (input + cache read + cache writes) exceeds
//     LongPromptThreshold per request; an aggregated record (RequestCount > 1)
//     is judged by its average request. A record with RequestCount 0 (request
//     count unknown, as in daily API usage buckets) cannot be judged and stays
//     on the standard tier; TierUncertain tells when that matters.
//   - A model without a price returns 0, estimated=false, known=false.
//   - A total-only record (no input/output split) that reported no cost
//     cannot be priced either and returns 0, estimated=false, known=false.
func (p *Pricer) Cost(r model.UsageRecord) (usd float64, estimated, known bool) {
	if r.ReportedCostUSD != nil {
		return *r.ReportedCostUSD, false, true
	}
	if r.TotalOnly {
		return 0, false, false
	}
	pr, ok := p.Lookup(r.Model)
	if !ok {
		return 0, false, false
	}
	price := pr.Price
	if pr.Long != nil {
		prompt := r.InputTokens + r.CacheReadTokens + r.CacheWrite5mTokens + r.CacheWrite1hTokens
		if r.RequestCount > 0 && prompt > LongPromptThreshold*r.RequestCount {
			price = *pr.Long
		}
	}
	return priceTokens(price, r.InputTokens, r.OutputTokens,
		r.CacheReadTokens, r.CacheWrite5mTokens, r.CacheWrite1hTokens), true, true
}

// TierUncertain reports whether Cost's result for r may be too low because the
// model has a long-prompt tier, the record's request count is unknown (0) and
// its prompt is large enough that it could have been billed at that tier.
func (p *Pricer) TierUncertain(r model.UsageRecord) bool {
	if r.ReportedCostUSD != nil || r.RequestCount > 0 {
		return false
	}
	pr, ok := p.Lookup(r.Model)
	if !ok || pr.Long == nil {
		return false
	}
	return r.InputTokens+r.CacheReadTokens+r.CacheWrite5mTokens+r.CacheWrite1hTokens > LongPromptThreshold
}

// Summary is the cost of a set of records.
type Summary struct {
	USD float64
	// Estimated is true when part of USD was computed from the price table
	// instead of being reported by the sources.
	Estimated bool
	// UnknownModels lists models that have unreported records but no price;
	// their usage is missing from USD. Sorted, without duplicates.
	UnknownModels []string
	// TierUncertainModels lists models with a long-prompt tier that have
	// records of unknown request count (0) and a prompt over the threshold.
	// Those were priced at the standard tier, so USD may be too low for them.
	// Sorted, without duplicates.
	TierUncertainModels []string
	// TotalOnlyRecords and TotalOnlyTokens count the records that carry only a
	// combined token count and reported no cost. They are missing from USD
	// (cost not computable, which is different from a cost of 0).
	TotalOnlyRecords int64
	TotalOnlyTokens  int64
}

// Summarize totals the cost of the per-model inputs returned by
// Pricer.CostInputs (or store.CostInputs with LongPromptThreshold).
func (p *Pricer) Summarize(inputs []store.CostInput) Summary {
	var s Summary
	unknown := map[string]struct{}{}
	uncertain := map[string]struct{}{}
	for _, in := range inputs {
		s.USD += in.ReportedCostUSD
		s.TotalOnlyRecords += in.TotalOnlyRecords
		s.TotalOnlyTokens += in.TotalOnlyTokens
		if in.UnreportedRecords == 0 {
			continue
		}
		pr, ok := p.Lookup(in.Model)
		if !ok {
			unknown[in.Model] = struct{}{}
			continue
		}
		s.Estimated = true
		std, long := in.TokenCounts, store.TokenCounts{}
		if pr.Long != nil {
			if in.TierUnknownRecords > 0 {
				uncertain[in.Model] = struct{}{}
			}
			long = in.Long
			std = store.TokenCounts{
				InputTokens:        in.InputTokens - long.InputTokens,
				OutputTokens:       in.OutputTokens - long.OutputTokens,
				CacheReadTokens:    in.CacheReadTokens - long.CacheReadTokens,
				CacheWrite5mTokens: in.CacheWrite5mTokens - long.CacheWrite5mTokens,
				CacheWrite1hTokens: in.CacheWrite1hTokens - long.CacheWrite1hTokens,
			}
			s.USD += priceTokens(*pr.Long, long.InputTokens, long.OutputTokens,
				long.CacheReadTokens, long.CacheWrite5mTokens, long.CacheWrite1hTokens)
		}
		s.USD += priceTokens(pr.Price, std.InputTokens, std.OutputTokens,
			std.CacheReadTokens, std.CacheWrite5mTokens, std.CacheWrite1hTokens)
	}
	if math.IsNaN(s.USD) || math.IsInf(s.USD, 0) {
		s.USD = 0
	}
	for m := range unknown {
		s.UnknownModels = append(s.UnknownModels, m)
	}
	sort.Strings(s.UnknownModels)
	for m := range uncertain {
		s.TierUncertainModels = append(s.TierUncertainModels, m)
	}
	sort.Strings(s.TierUncertainModels)
	return s
}
