package pricing

import (
	"context"
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

func near(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

func fp(v float64) *float64 { return &v }

func mustPricer(t *testing.T, overrides ...store.PriceOverride) *Pricer {
	t.Helper()
	p, err := New(overrides)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return p
}

// sonnetRecord uses claude-sonnet-5-5: $2 in, $10 out, $0.10 cache read (0.05x),
// $2.50 5m cache write, $4 1h cache write, per million tokens.
func sonnetRecord(modelName string) model.UsageRecord {
	return model.UsageRecord{
		Source:             model.SourceClaudeCode,
		ExternalID:         "x",
		Timestamp:          time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC),
		Model:              modelName,
		InputTokens:        200_000, // 0.2 M * 2.00  = 0.40
		OutputTokens:       50_000,  // 0.05 M * 10   = 0.50
		CacheReadTokens:    1_000_000,
		CacheWrite5mTokens: 100_000, // 0.1 M * 2.50 = 0.25
		CacheWrite1hTokens: 40_000,  // 0.04 M * 4   = 0.16
		ThinkingTokens:     30_000,  // already inside output: not priced
		RequestCount:       1,
	}
	// cache read: 1 M * 0.10 = 0.10; total = 0.40+0.50+0.10+0.25+0.16 = 1.41
}

func TestCostMatchesHandComputedValue(t *testing.T) {
	p := mustPricer(t)
	usd, estimated, known := p.Cost(sonnetRecord("claude-sonnet-5-5"))
	if !known || !estimated {
		t.Fatalf("known=%v estimated=%v, want true, true", known, estimated)
	}
	if !near(usd, 1.41) {
		t.Fatalf("cost = %v, want 1.41", usd)
	}
}

func TestCostOpus55CacheBreakdown(t *testing.T) {
	p := mustPricer(t)
	r := model.UsageRecord{
		Model:              "claude-opus-5-5", // 4 / 20 / 0.20 / 5.00 / 8.00
		InputTokens:        1_000_000,         // 4.00
		OutputTokens:       500_000,           // 10.00
		CacheReadTokens:    2_000_000,         // 0.40
		CacheWrite5mTokens: 1_000_000,         // 5.00
		CacheWrite1hTokens: 500_000,           // 4.00
	}
	usd, _, known := p.Cost(r)
	if !known || !near(usd, 23.40) {
		t.Fatalf("cost = %v known=%v, want 23.40", usd, known)
	}
}

func TestCostReportedWins(t *testing.T) {
	p := mustPricer(t)
	r := sonnetRecord("claude-sonnet-5-5")
	r.ReportedCostUSD = fp(0.42)
	usd, estimated, known := p.Cost(r)
	if usd != 0.42 || estimated || !known {
		t.Fatalf("got %v estimated=%v known=%v, want 0.42 false true", usd, estimated, known)
	}

	// A reported zero is a reported cost, not "missing".
	r.ReportedCostUSD = fp(0)
	if usd, estimated, known := p.Cost(r); usd != 0 || estimated || !known {
		t.Fatalf("reported zero: got %v estimated=%v known=%v", usd, estimated, known)
	}

	// A reported cost is used even for a model without a price.
	r.Model = "mystery-model"
	r.ReportedCostUSD = fp(1.25)
	if usd, estimated, known := p.Cost(r); usd != 1.25 || estimated || !known {
		t.Fatalf("reported, unknown model: got %v estimated=%v known=%v", usd, estimated, known)
	}
}

func TestCostUnknownModel(t *testing.T) {
	p := mustPricer(t)
	for _, name := range []string{"", "mystery-model", "claude-opus-5-6"} {
		usd, estimated, known := p.Cost(sonnetRecord(name))
		if known || estimated || usd != 0 {
			t.Errorf("model %q: got %v estimated=%v known=%v, want 0 false false", name, usd, estimated, known)
		}
	}
}

func TestModelMatching(t *testing.T) {
	p := mustPricer(t)
	want := func(name, canonical string) {
		t.Helper()
		got, ok := p.Lookup(name)
		exp, expOK := p.Lookup(canonical)
		if !ok || !expOK {
			t.Fatalf("Lookup(%q) ok=%v, Lookup(%q) ok=%v", name, ok, canonical, expOK)
		}
		if got.Price != exp.Price {
			t.Errorf("Lookup(%q) = %+v, want the price of %q (%+v)", name, got.Price, canonical, exp.Price)
		}
	}
	want("claude-sonnet-5-5-20260101", "claude-sonnet-5-5") // date suffix
	want("claude-haiku-4-5-20251001", "claude-haiku-4-5")
	want("claude-sonnet-4-6@20260101", "claude-sonnet-4-6")
	want("Claude-Opus-4-8", "claude-opus-4-8")     // case
	want("claude-opus-4-8[1m]", "claude-opus-4-8") // context tag
	want("anthropic/claude-haiku-5-5", "claude-haiku-5-5")
	want("gpt-5-2025-08-07", "gpt-5")
	want("gemini-2.5-pro-latest", "gemini-2.5-pro")
	want("models/gemini-2.5-flash", "gemini-2.5-flash")

	// An exact entry must beat a shorter one: 5-5 is not 5.
	a, _ := p.Lookup("claude-opus-5-5")
	b, _ := p.Lookup("claude-opus-5")
	if a.Price == b.Price {
		t.Error("claude-opus-5-5 resolved to the claude-opus-5 price")
	}
	// A newer, unlisted version must not silently take an older price.
	if _, ok := p.Lookup("claude-opus-5-6"); ok {
		t.Error("claude-opus-5-6 should be unknown")
	}
	if _, ok := p.Lookup("gpt-5.4-turbo"); ok {
		t.Error("gpt-5.4-turbo should be unknown")
	}
}

func TestOverrideBeatsDefault(t *testing.T) {
	p := mustPricer(t, store.PriceOverride{
		Model:        "claude-sonnet-5-5",
		InputPerMTok: fp(10), // default 2.00
		// other components not overridden: defaults stay
	})
	got, ok := p.Lookup("claude-sonnet-5-5")
	if !ok || !got.Overridden {
		t.Fatalf("Lookup = %+v ok=%v, want overridden", got, ok)
	}
	if got.Input != 10 || got.Output != 10 || got.CacheRead != 0.1 {
		t.Fatalf("price = %+v, want input 10 with default output/cache", got.Price)
	}
	// 0.2 M * 10 + 0.5 + 0.1 + 0.25 + 0.16 = 3.01
	usd, _, _ := p.Cost(sonnetRecord("claude-sonnet-5-5"))
	if !near(usd, 3.01) {
		t.Fatalf("cost = %v, want 3.01", usd)
	}
	// The override also covers dated variants of the model.
	usd, _, _ = p.Cost(sonnetRecord("claude-sonnet-5-5-20260101"))
	if !near(usd, 3.01) {
		t.Fatalf("dated variant cost = %v, want 3.01", usd)
	}
	// Other models are untouched.
	if d, _ := p.Lookup("claude-sonnet-4-6"); d.Overridden || d.Input != 3 {
		t.Fatalf("claude-sonnet-4-6 = %+v", d)
	}
}

func TestOverrideForUnlistedModel(t *testing.T) {
	p := mustPricer(t,
		store.PriceOverride{Model: "my-local-model", InputPerMTok: fp(1), OutputPerMTok: fp(3)},
		store.PriceOverride{Model: "cache-only", CacheReadPerMTok: fp(1)},
	)
	r := model.UsageRecord{Model: "my-local-model", InputTokens: 2_000_000, OutputTokens: 1_000_000, CacheReadTokens: 5_000_000}
	usd, estimated, known := p.Cost(r)
	if !known || !estimated || !near(usd, 5.0) { // 2 + 3, unset cache price counts as 0
		t.Fatalf("cost = %v estimated=%v known=%v, want 5 true true", usd, estimated, known)
	}
	// An override that does not set both input and output is not a usable price
	// for an unlisted model: the missing component would be priced at 0.
	if _, ok := p.Lookup("cache-only"); ok {
		t.Error("cache-only override should not make the model known")
	}
	partial := mustPricer(t, store.PriceOverride{Model: "input-only", InputPerMTok: fp(3)})
	if usd, estimated, known := partial.Cost(model.UsageRecord{Model: "input-only", InputTokens: 1_000_000, OutputTokens: 1_000_000}); known || estimated || usd != 0 {
		t.Errorf("input-only override for an unlisted model: got %v estimated=%v known=%v, want 0 false false", usd, estimated, known)
	}
	// For a listed model a partial override is fine: the default fills the rest.
	if _, ok := mustPricer(t, store.PriceOverride{Model: "claude-sonnet-5-5", InputPerMTok: fp(3)}).Lookup("claude-sonnet-5-5"); !ok {
		t.Error("partial override of a listed model must stay known")
	}
}

func TestDefaultsAreSane(t *testing.T) {
	tbl, err := loadDefaults()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := time.Parse("2006-01-02", tbl.AsOf); err != nil {
		t.Errorf("as_of %q is not a date: %v", tbl.AsOf, err)
	}
	services := map[string]bool{model.ServiceAnthropic: true, model.ServiceOpenAI: true, model.ServiceGoogle: true}
	seen := map[string]bool{}
	for name, e := range tbl.Models {
		keys := modelKeys(name)
		if len(keys) == 0 || keys[0] != name {
			t.Errorf("%q is not in normalized form (lower case, no date suffix)", name)
		}
		if len(keys) > 1 {
			t.Errorf("%q ends in a date/latest suffix; defaults must hold base names", name)
		}
		seen[e.Service] = true
		if !services[e.Service] {
			t.Errorf("%q: unknown service %q", name, e.Service)
		}
		if e.Source == "" {
			t.Errorf("%q: no source recorded", name)
		}
		if e.Input <= 0 || e.Output <= 0 || e.CacheRead < 0 || e.CacheWrite5m < 0 || e.CacheWrite1h < 0 {
			t.Errorf("%q: invalid prices %+v", name, e.Price)
		}
		if e.Output < e.Input {
			t.Errorf("%q: output price below input price: %+v", name, e.Price)
		}
		if e.Service == model.ServiceAnthropic {
			// Anthropic cache pricing: write 1.25x (5m) / 2x (1h); read 0.1x, except
			// 0.05x on Opus 5.5 / Sonnet 5.5 and 0.025x on Fable 5.1 / Mythos 5.1.
			readMult := 0.1
			switch name {
			case "claude-opus-5-5", "claude-sonnet-5-5":
				readMult = 0.05
			case "claude-fable-5-1", "claude-mythos-5-1":
				readMult = 0.025
			}
			if !near(e.CacheWrite5m, e.Input*1.25) || !near(e.CacheWrite1h, e.Input*2) || !near(e.CacheRead, e.Input*readMult) {
				t.Errorf("%q: cache prices do not follow the Anthropic multipliers: %+v", name, e.Price)
			}
		}
	}
	for s := range services {
		if !seen[s] {
			t.Errorf("no default prices for service %q", s)
		}
	}
	// Spot-check the documented Anthropic list prices.
	spot := map[string][2]float64{
		"claude-opus-5-5": {4, 20}, "claude-sonnet-5-5": {2, 10}, "claude-haiku-5-5": {0.10, 0.50},
		"claude-haiku-4-5": {1, 5}, "claude-fable-5-1": {10, 50},
		"claude-opus-4-1": {15, 75}, "claude-opus-4": {15, 75}, "claude-sonnet-4": {3, 15},
	}
	for name, w := range spot {
		if e := tbl.Models[name]; e.Input != w[0] || e.Output != w[1] {
			t.Errorf("%s = %v/%v, want %v/%v", name, e.Input, e.Output, w[0], w[1])
		}
	}
	// Cache-read prices from the official Anthropic pricing page (Sonnet 5.5 is
	// $0.10, not $0.20: footnote 2, 0.05x of the base input price).
	reads := map[string]float64{
		"claude-sonnet-5-5": 0.10, "claude-sonnet-5": 0.20, "claude-opus-5-5": 0.20, "claude-opus-5": 0.50,
		"claude-haiku-5-5": 0.01, "claude-fable-5-1": 0.25, "claude-fable-5": 1.00, "claude-sonnet-4-6": 0.30,
	}
	for name, want := range reads {
		if got := tbl.Models[name].CacheRead; !near(got, want) {
			t.Errorf("%s cache_read = %v, want %v", name, got, want)
		}
	}
	// OpenAI GPT-5.6 charges for cache writes: 30m write -> cache_write_5m,
	// 12h write -> cache_write_1h.
	writes := map[string][2]float64{
		"gpt-5.6-sol": {5.00, 10.00}, "gpt-5.6-terra": {2.50, 5.00}, "gpt-5.6-luna": {0.25, 0.50},
	}
	for name, w := range writes {
		e := tbl.Models[name]
		if !near(e.CacheWrite5m, w[0]) || !near(e.CacheWrite1h, w[1]) {
			t.Errorf("%s writes = %v/%v, want %v/%v", name, e.CacheWrite5m, e.CacheWrite1h, w[0], w[1])
		}
	}
	// Optional fields are well formed.
	for name, e := range tbl.Models {
		if e.ValidUntil != "" {
			if _, err := time.Parse("2006-01-02", e.ValidUntil); err != nil {
				t.Errorf("%s: valid_until %q: %v", name, e.ValidUntil, err)
			}
		}
		if lp := e.LongPrompt; lp != nil && (lp.Input < e.Input || lp.Output < e.Output) {
			t.Errorf("%s: long_prompt prices are below the standard prices", name)
		}
	}
}

func TestRetiredModelsAreKnown(t *testing.T) {
	p := mustPricer(t)
	for _, name := range []string{"claude-opus-4-1-20250805", "claude-opus-4-20250514", "claude-sonnet-4-20250514"} {
		if _, ok := p.Lookup(name); !ok {
			t.Errorf("%s should be priced", name)
		}
	}
	// claude-opus-4 must not swallow the 4.x point releases.
	a, _ := p.Lookup("claude-opus-4-20250514")
	b, _ := p.Lookup("claude-opus-4-8")
	if a.Price == b.Price {
		t.Error("claude-opus-4-8 resolved to the claude-opus-4 price")
	}
}

func TestOpenAICacheWriteCost(t *testing.T) {
	p := mustPricer(t)
	r := model.UsageRecord{Model: "gpt-5.6-sol", CacheWrite5mTokens: 1_000_000, CacheWrite1hTokens: 1_000_000}
	if usd, _, known := p.Cost(r); !known || !near(usd, 15.00) { // 5.00 + 10.00
		t.Fatalf("cost = %v known=%v, want 15.00", usd, known)
	}
}

func TestLongPromptTier(t *testing.T) {
	p := mustPricer(t)
	// claude-haiku-5-5: up to 100K prompt 0.10 / 0.50 / 0.01; over 100K 0.50 / 2.50 / 0.05.
	mk := func(in, cr, out, requests int64) model.UsageRecord {
		return model.UsageRecord{Model: "claude-haiku-5-5", InputTokens: in, CacheReadTokens: cr, OutputTokens: out, RequestCount: requests}
	}
	cases := []struct {
		name string
		r    model.UsageRecord
		want float64
	}{
		// prompt = 100,000 exactly: not over the threshold
		{"at threshold", mk(50_000, 50_000, 10_000, 1), 0.05*0.10 + 0.05*0.01 + 0.01*0.50},
		// prompt = 100,001: long tier; cache reads count toward the prompt
		{"cache read pushes it over", mk(50_000, 50_001, 10_000, 1), 0.05*0.50 + 0.050001*0.05 + 0.01*2.50},
		{"far over", mk(200_000, 0, 20_000, 1), 0.2*0.50 + 0.02*2.50},
		// two requests of 75K average: standard tier
		{"aggregated record judged per request", mk(150_000, 0, 20_000, 2), 0.15*0.10 + 0.02*0.50},
		// two requests of 150K average: long tier
		{"aggregated long", mk(300_000, 0, 20_000, 2), 0.3*0.50 + 0.02*2.50},
		// RequestCount 0 = unknown (e.g. a daily API bucket): never judged long
		{"no request count", mk(200_000, 0, 0, 0), 0.2 * 0.10},
		{"no request count, huge prompt", mk(1_000_000, 0, 100_000, 0), 1.0*0.10 + 0.1*0.50},
	}
	for _, tc := range cases {
		usd, estimated, known := p.Cost(tc.r)
		if !known || !estimated || !near(usd, tc.want) {
			t.Errorf("%s: cost = %v (known=%v), want %v", tc.name, usd, known, tc.want)
		}
	}

	// Models without a long tier are unaffected by prompt size.
	big := model.UsageRecord{Model: "claude-sonnet-5-5", InputTokens: 500_000, RequestCount: 1}
	if usd, _, _ := p.Cost(big); !near(usd, 1.00) {
		t.Errorf("sonnet 500K prompt = %v, want 1.00", usd)
	}

	// An override replaces the component in both tiers; the other components
	// keep their own tier's price.
	po := mustPricer(t, store.PriceOverride{Model: "claude-haiku-5-5", InputPerMTok: fp(1)})
	if usd, _, _ := po.Cost(mk(200_000, 0, 0, 1)); !near(usd, 0.2) { // long tier, input overridden
		t.Errorf("overridden haiku input, long = %v, want 0.2", usd)
	}
	if usd, _, _ := po.Cost(mk(50_000, 0, 10_000, 1)); !near(usd, 0.05*1+0.01*0.50) { // standard tier
		t.Errorf("overridden haiku input, standard = %v, want 0.055", usd)
	}
	if usd, _, _ := po.Cost(mk(200_000, 0, 20_000, 1)); !near(usd, 0.2*1+0.02*2.50) { // long output stays 2.50
		t.Errorf("overridden haiku input keeps the long output price: %v, want 0.25", usd)
	}
}

func TestPartialOverrideKeepsLongTier(t *testing.T) {
	// Only cache_read is overridden (to the default value): nothing may change.
	base := mustPricer(t)
	po := mustPricer(t, store.PriceOverride{Model: "claude-haiku-5-5", CacheReadPerMTok: fp(0.01)})
	r := model.UsageRecord{Model: "claude-haiku-5-5", InputTokens: 200_000, OutputTokens: 20_000, RequestCount: 1}
	a, _, _ := base.Cost(r)
	b, _, _ := po.Cost(r)
	if !near(a, b) || !near(a, 0.2*0.50+0.02*2.50) {
		t.Fatalf("base = %v, with cache-only override = %v, want both 0.15", a, b)
	}
	got, _ := po.Lookup("claude-haiku-5-5")
	if got.Long == nil || got.Long.Input != 0.50 || got.Long.CacheRead != 0.01 || !got.Overridden {
		t.Fatalf("Long = %+v overridden=%v", got.Long, got.Overridden)
	}
	// The shared table entry must not have been modified.
	if again, _ := base.Lookup("claude-haiku-5-5"); again.Long.CacheRead != 0.05 {
		t.Fatalf("override leaked into the table: %+v", again.Long)
	}
}

func TestUnknownRequestCountStaysStandardAndIsFlagged(t *testing.T) {
	p := mustPricer(t)
	// A daily API bucket: 200 requests of 5K tokens, but request_count unknown.
	r := model.UsageRecord{Model: "claude-haiku-5-5", InputTokens: 1_000_000, RequestCount: 0}
	if usd, _, _ := p.Cost(r); !near(usd, 0.10) { // standard tier, not the 0.50 long tier
		t.Fatalf("cost = %v, want 0.10", usd)
	}
	if !p.TierUncertain(r) {
		t.Error("TierUncertain = false for a large prompt with unknown request count")
	}
	small := r
	small.InputTokens = 50_000
	known := r
	known.RequestCount = 200
	other := model.UsageRecord{Model: "claude-sonnet-5-5", InputTokens: 1_000_000}
	reported := r
	reported.ReportedCostUSD = fp(1)
	for name, rr := range map[string]model.UsageRecord{"small prompt": small, "known count": known, "no long tier": other, "reported": reported} {
		if p.TierUncertain(rr) {
			t.Errorf("%s: TierUncertain = true, want false", name)
		}
	}
}

func TestSummarizeLongPromptTier(t *testing.T) {
	p := mustPricer(t)
	in := store.CostInput{
		Model: "claude-haiku-5-5",
		// 300K input and 30K output in total, of which 200K input / 20K output
		// come from long-prompt records.
		TokenCounts:       store.TokenCounts{InputTokens: 300_000, OutputTokens: 30_000},
		Long:              store.TokenCounts{InputTokens: 200_000, OutputTokens: 20_000},
		UnreportedRecords: 2,
	}
	// standard: 0.1 M * 0.10 + 0.01 M * 0.50 = 0.015; long: 0.2 M * 0.50 + 0.02 M * 2.50 = 0.15
	if s := p.Summarize([]store.CostInput{in}); !near(s.USD, 0.165) || !s.Estimated {
		t.Fatalf("summary = %+v, want 0.165", s)
	}
	// A model without a long tier prices everything at the standard rate.
	in.Model = "claude-sonnet-5-5" // 0.3 * 2 + 0.03 * 10 = 0.9
	if s := p.Summarize([]store.CostInput{in}); !near(s.USD, 0.9) {
		t.Fatalf("sonnet summary = %+v, want 0.9", s)
	}
}

func TestExpired(t *testing.T) {
	p := mustPricer(t)
	r, ok := p.Lookup("gpt-5.6-sol")
	if !ok || r.ValidUntil != "2026-11-30" {
		t.Fatalf("Lookup = %+v ok=%v", r, ok)
	}
	day := func(y int, m time.Month, d, h int) time.Time { return time.Date(y, m, d, h, 0, 0, 0, time.UTC) }
	if r.Expired(day(2026, 11, 30, 23)) {
		t.Error("the last valid day must not count as expired")
	}
	if !r.Expired(day(2026, 12, 1, 0)) {
		t.Error("the day after valid_until must be expired")
	}
	if c, _ := p.Lookup("claude-sonnet-5-5"); c.Expired(day(2030, 1, 1, 0)) {
		t.Error("an entry without valid_until never expires")
	}
	// A user override has no end date.
	po := mustPricer(t, store.PriceOverride{Model: "gpt-5.6-sol", InputPerMTok: fp(1)})
	if o, _ := po.Lookup("gpt-5.6-sol"); o.Expired(day(2027, 1, 1, 0)) {
		t.Error("an overridden price must not be reported as expired")
	}
}

func TestSummarize(t *testing.T) {
	p := mustPricer(t)
	inputs := []store.CostInput{
		{ // 1.41 estimated from tokens, plus 2.00 reported by other records
			Model: "claude-sonnet-5-5-20260101",
			TokenCounts: store.TokenCounts{InputTokens: 200_000, OutputTokens: 50_000,
				CacheReadTokens: 1_000_000, CacheWrite5mTokens: 100_000, CacheWrite1hTokens: 40_000},
			UnreportedRecords: 3, ReportedRecords: 2, ReportedCostUSD: 2.00,
		},
		{Model: "mystery-model", TokenCounts: store.TokenCounts{InputTokens: 999}, UnreportedRecords: 1},
		{Model: "another-unknown", ReportedRecords: 1, ReportedCostUSD: 0.5}, // reported: no price needed
	}
	s := p.Summarize(inputs)
	if !near(s.USD, 3.91) {
		t.Errorf("USD = %v, want 3.91", s.USD)
	}
	if !s.Estimated {
		t.Error("Estimated = false, want true")
	}
	if len(s.UnknownModels) != 1 || s.UnknownModels[0] != "mystery-model" {
		t.Errorf("UnknownModels = %v, want [mystery-model]", s.UnknownModels)
	}

	// Only reported costs: exact, not estimated.
	s = p.Summarize([]store.CostInput{{Model: "x", ReportedRecords: 1, ReportedCostUSD: 1.5}})
	if s.USD != 1.5 || s.Estimated || len(s.UnknownModels) != 0 {
		t.Errorf("reported-only summary = %+v", s)
	}
	if s := p.Summarize(nil); s.USD != 0 || s.Estimated {
		t.Errorf("empty summary = %+v", s)
	}
}

func TestLoadUsesStoredOverrides(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	if err := st.SetPriceOverride(ctx, store.PriceOverride{Model: "claude-sonnet-5-5", OutputPerMTok: fp(100)}); err != nil {
		t.Fatal(err)
	}
	p, err := Load(ctx, st)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := p.Lookup("claude-sonnet-5-5")
	if got.Output != 100 || got.Input != 2 || !got.Overridden {
		t.Fatalf("Lookup = %+v", got)
	}

	// Deleting the override restores the default.
	if err := st.DeletePriceOverride(ctx, "claude-sonnet-5-5"); err != nil {
		t.Fatal(err)
	}
	p, _ = Load(ctx, st)
	if got, _ := p.Lookup("claude-sonnet-5-5"); got.Output != 10 || got.Overridden {
		t.Fatalf("after delete Lookup = %+v", got)
	}
}

func TestListDefaultsIsSorted(t *testing.T) {
	list := mustPricer(t).ListDefaults()
	if len(list) < 30 {
		t.Fatalf("only %d defaults", len(list))
	}
	for i := 1; i < len(list); i++ {
		a, b := list[i-1], list[i]
		if a.Service > b.Service || (a.Service == b.Service && a.Model >= b.Model) {
			t.Fatalf("not sorted at %d: %s/%s then %s/%s", i, a.Service, a.Model, b.Service, b.Model)
		}
	}
}

func TestSummarizeFromStoreLongPrompt(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "p.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	ts := time.Date(2026, 11, 6, 10, 0, 0, 0, time.UTC)
	rec := func(id string, in, out int64) model.UsageRecord {
		return model.UsageRecord{Source: model.SourceClaudeCode, ExternalID: id, Timestamp: ts,
			Model: "claude-haiku-5-5", InputTokens: in, OutputTokens: out, RequestCount: 1}
	}
	if _, _, err := st.InsertBatch(ctx, []model.UsageRecord{
		rec("short", 50_000, 10_000), // standard: 0.05*0.10 + 0.01*0.50 = 0.010
		rec("long", 200_000, 20_000), // long:     0.20*0.50 + 0.02*2.50 = 0.150
	}); err != nil {
		t.Fatal(err)
	}
	p := mustPricer(t)
	inputs, err := p.CostInputs(ctx, st, store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	if s := p.Summarize(inputs); !near(s.USD, 0.160) || len(s.TierUncertainModels) != 0 {
		t.Fatalf("summary = %+v, want 0.160 and no uncertain models", s)
	}

	// A daily bucket without a request count: standard tier, flagged.
	if _, _, err := st.InsertBatch(ctx, []model.UsageRecord{{
		Source: model.SourceAnthropicAPI, ExternalID: "bucket", Timestamp: ts,
		Model: "claude-haiku-5-5", InputTokens: 1_000_000, // RequestCount 0 = unknown
	}}); err != nil {
		t.Fatal(err)
	}
	inputs, err = p.CostInputs(ctx, st, store.Filter{})
	if err != nil {
		t.Fatal(err)
	}
	s := p.Summarize(inputs)
	if !near(s.USD, 0.160+0.10) { // the bucket adds 1M * 0.10, not 1M * 0.50
		t.Fatalf("USD = %v, want 0.260", s.USD)
	}
	if len(s.TierUncertainModels) != 1 || s.TierUncertainModels[0] != "claude-haiku-5-5" {
		t.Fatalf("TierUncertainModels = %v", s.TierUncertainModels)
	}

	// Per-record Cost agrees with the aggregated figure.
	var sum float64
	for _, r := range []model.UsageRecord{rec("a", 50_000, 10_000), rec("b", 200_000, 20_000)} {
		usd, _, _ := p.Cost(r)
		sum += usd
	}
	if !near(sum, 0.160) {
		t.Fatalf("sum of Cost = %v, want 0.160", sum)
	}
}

func TestTotalOnlyRecordsAreNotPriced(t *testing.T) {
	p := mustPricer(t)
	r := model.UsageRecord{
		Model: "gpt-5.5", RequestCount: 1, TotalOnly: true, TotalOnlyTokens: 5_000_000,
	}
	if usd, estimated, known := p.Cost(r); usd != 0 || estimated || known {
		t.Fatalf("Cost = %v, %v, %v; want 0, false, false (not computable)", usd, estimated, known)
	}
	// A cost the source reported itself still wins.
	r.ReportedCostUSD = fp(1.25)
	if usd, estimated, known := p.Cost(r); usd != 1.25 || estimated || !known {
		t.Fatalf("reported cost = %v, %v, %v; want 1.25, false, true", usd, estimated, known)
	}
}

func TestSummarizeReportsTotalOnlyAsMissingFromCost(t *testing.T) {
	p := mustPricer(t)
	s := p.Summarize([]store.CostInput{
		{Model: "gpt-5.5", TotalOnlyRecords: 3, TotalOnlyTokens: 9_000_000}, // nothing priceable
		{Model: "claude-sonnet-5-5", TokenCounts: store.TokenCounts{InputTokens: 1_000_000}, UnreportedRecords: 1},
	})
	if !near(s.USD, 2.00) || !s.Estimated {
		t.Fatalf("USD = %v estimated=%v, want 2.00 (only the split record) and true", s.USD, s.Estimated)
	}
	if s.TotalOnlyRecords != 3 || s.TotalOnlyTokens != 9_000_000 || len(s.UnknownModels) != 0 {
		t.Fatalf("summary = %+v; total-only must be counted and the model must not be 'unknown'", s)
	}
}
