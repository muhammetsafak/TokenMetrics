package openaiapi

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/httpx"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/secrets"
)

const (
	// SettingLastBucket is the settings key holding the start (unix seconds)
	// of the newest bucket fetched by the last fully successful sync.
	SettingLastBucket = "collector.openai_api.last_bucket"

	// SettingLastCostBucket is the same for the cost report. It is separate so
	// that a failing cost request never holds usage back.
	SettingLastCostBucket = "collector.openai_api.last_cost_bucket"

	// DefaultInitialLookback is how far back the first sync reaches.
	DefaultInitialLookback = 90 * 24 * time.Hour
)

// Collector implements collector.Collector for the OpenAI Organization API.
type Collector struct {
	baseURL     string
	keyFn       func() (string, error)
	width       string
	includeCost bool
	lookback    time.Duration
	httpCfg     httpx.Config
	now         func() time.Time
}

// Option configures a Collector.
type Option func(*Collector)

// WithBaseURL overrides the API host (tests use an httptest server).
func WithBaseURL(u string) Option { return func(c *Collector) { c.baseURL = u } }

// WithKeyFunc replaces the keychain lookup.
func WithKeyFunc(f func() (string, error)) Option { return func(c *Collector) { c.keyFn = f } }

// WithBucketWidth sets the usage bucket width (1d or 1h). The costs endpoint
// is daily only, so cost is fetched only for 1d. Records of different widths
// have different IDs: changing the width after a sync double counts the
// already stored period, so treat it as a one-time choice.
func WithBucketWidth(w string) Option { return func(c *Collector) { c.width = w } }

// WithCost enables fetching the costs endpoint into ReportedCostUSD (1d only).
func WithCost(on bool) Option { return func(c *Collector) { c.includeCost = on } }

// WithInitialLookback sets how far back the first sync reaches.
func WithInitialLookback(d time.Duration) Option { return func(c *Collector) { c.lookback = d } }

// WithHTTPConfig sets the base HTTP client configuration. The admin key is
// added to its Redact list on every run.
func WithHTTPConfig(cfg httpx.Config) Option { return func(c *Collector) { c.httpCfg = cfg } }

// WithClock replaces time.Now (tests).
func WithClock(now func() time.Time) Option { return func(c *Collector) { c.now = now } }

// New returns a collector reading the key from the OS keychain.
func New(opts ...Option) *Collector {
	c := &Collector{
		baseURL:  DefaultBaseURL,
		keyFn:    func() (string, error) { return secrets.Get(secrets.ProviderOpenAI) },
		width:    Width1d,
		lookback: DefaultInitialLookback,
		now:      time.Now,
		httpCfg:  httpx.Config{UserAgent: "TokenMetrics"},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// Name implements collector.Collector.
func (c *Collector) Name() model.Source { return model.SourceOpenAIAPI }

var (
	usageGroupBy = []string{"model", "project_id"}
	costGroupBy  = []string{"project_id", "line_item"}
)

// Collect implements collector.Collector.
//
// Usage and cost have independent sync positions. Each starts from the newest
// bucket its last successful fetch returned (that bucket may have been
// partial), so a bucket is updated, never duplicated: records are keyed by
// (bucket, model, project). A failing costs endpoint therefore never holds
// usage back, and a failed or disabled cost fetch never erases a cost that an
// earlier sync stored.
func (c *Collector) Collect(ctx context.Context, deps collector.Deps) (collector.Result, error) {
	var res collector.Result
	log := deps.Logger
	if log == nil {
		log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if deps.Store == nil {
		return res, errors.New("openaiapi: store is nil")
	}

	key, err := c.keyFn()
	switch {
	case errors.Is(err, secrets.ErrNotFound):
		res.Warnings = append(res.Warnings, "openai admin key is not configured; skipped")
		return res, nil
	case err != nil:
		return res, fmt.Errorf("openaiapi: read admin key: %w", err)
	}

	cfg := c.httpCfg
	cfg.Redact = append(append([]string(nil), cfg.Redact...), key)
	client, err := NewClient(c.baseURL, key, httpx.New(cfg))
	if err != nil {
		return res, err
	}

	start, err := c.startTime(ctx, deps, SettingLastBucket)
	if err != nil {
		return res, err
	}
	log.Info("openai usage sync starting", "from", start.Format(time.RFC3339), "bucket_width", c.width)

	usage, pages, fetchErr := client.FetchUsage(ctx, start, c.width, usageGroupBy)
	res.Files += pages

	if c.includeCost && c.width != Width1d {
		res.Warnings = append(res.Warnings, "costs endpoint is daily only; cost skipped for bucket width "+c.width)
	}
	var costs []CostBucket
	costsOK := false
	if c.includeCost && c.width == Width1d && fetchErr == nil && ctx.Err() == nil {
		costStart, err := c.startTime(ctx, deps, SettingLastCostBucket)
		if err != nil {
			return res, err
		}
		var costPages int
		var costErr error
		costs, costPages, costErr = client.FetchCost(ctx, costStart, costGroupBy)
		res.Files += costPages
		if costErr != nil {
			res.Warnings = append(res.Warnings, "costs request failed; stored costs kept: "+costErr.Error())
			costs = nil
		} else {
			costsOK = true
		}
	}
	// A canceled run writes nothing (the store would refuse anyway) and keeps
	// its positions, so the next run starts from the same place.
	if err := ctx.Err(); err != nil {
		return res, err
	}

	lookup := func(id string) (model.UsageRecord, bool, error) {
		return deps.Store.GetUsageRecord(ctx, model.SourceOpenAIAPI, id)
	}
	records, err := c.buildRecords(deps.DeviceID, usage, costs, costsOK, lookup)
	if err != nil {
		return res, fmt.Errorf("openaiapi: %w", err)
	}
	added, updated, err := deps.Store.InsertBatch(ctx, records)
	res.Added, res.Updated = added, updated
	if err != nil {
		return res, fmt.Errorf("openaiapi: store records: %w", err)
	}
	if fetchErr != nil {
		return res, fetchErr
	}

	if len(usage) > 0 {
		newest := usage[0].StartTime
		for _, b := range usage {
			if b.StartTime > newest {
				newest = b.StartTime
			}
		}
		if err := deps.Store.SetSetting(ctx, SettingLastBucket, newest); err != nil {
			return res, fmt.Errorf("openaiapi: save sync position: %w", err)
		}
	}
	if costsOK && len(costs) > 0 {
		newest := costs[0].StartTime
		for _, b := range costs {
			if b.StartTime > newest {
				newest = b.StartTime
			}
		}
		if err := deps.Store.SetSetting(ctx, SettingLastCostBucket, newest); err != nil {
			return res, fmt.Errorf("openaiapi: save cost sync position: %w", err)
		}
	}
	log.Info("openai usage sync finished", "added", res.Added, "updated", res.Updated, "pages", res.Files)
	return res, nil
}

func (c *Collector) startTime(ctx context.Context, deps collector.Deps, settingKey string) (time.Time, error) {
	var last int64
	found, err := deps.Store.GetSetting(ctx, settingKey, &last)
	if err != nil {
		return time.Time{}, fmt.Errorf("openaiapi: read sync position: %w", err)
	}
	if found && last > 0 {
		return time.Unix(last, 0).UTC(), nil
	}
	return c.now().Add(-c.lookback).UTC().Truncate(24 * time.Hour), nil
}

type recordKey struct {
	start   int64
	model   string
	project string
}

func (k recordKey) id() string {
	return externalID(time.Unix(k.start, 0), k.model, k.project)
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// externalID is openai:<bucket start>:<model>:<project>; the same bucket
// fetched again maps to the same record, which the store then updates.
func externalID(start time.Time, modelName, project string) string {
	return "openai:" + start.UTC().Format(time.RFC3339) + ":" + modelName + ":" + project
}

// costOnlyID is the ID of the token-less record that carries a cost for which
// no usage row exists (other products, or a differently named model).
func costOnlyID(usageID string) string { return usageID + ":cost" }

// lineItemModel extracts the model from a cost line item such as
// "gpt-4o-2024-08-06, input"; items without a comma are used whole.
func lineItemModel(lineItem string) string {
	if i := strings.LastIndex(lineItem, ", "); i > 0 {
		return strings.TrimSpace(lineItem[:i])
	}
	return strings.TrimSpace(lineItem)
}

func clamp0(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

type lookupFunc func(id string) (model.UsageRecord, bool, error)

// buildRecords turns fetched buckets into records to upsert.
//
// Cost rows are summed per (day, model, project) and attached to the usage
// record of that key: the one from this fetch, or else the one already stored
// (the cost window can reach further back than the usage window). Only a key
// with no usage record anywhere gets a cost-only record; once a usage record
// shows up later, the cost-only record is reset so the cost is not counted
// twice. A usage record without fresh cost data keeps the cost already stored
// for it.
func (c *Collector) buildRecords(deviceID string, usage []UsageBucket, costs []CostBucket, costsOK bool, lookup lookupFunc) ([]model.UsageRecord, error) {
	var out []model.UsageRecord
	var keys []recordKey
	index := map[recordKey]int{}

	for _, b := range usage {
		start := time.Unix(b.StartTime, 0).UTC()
		for _, r := range b.Results {
			// OpenAI's input_tokens already contains cached and cache-write
			// tokens; the store keeps the three separately, so split them.
			// The short retention window (30 minutes) fills the 5-minute
			// slot and the long one (12 hours) the 1-hour slot; when only
			// the total is reported, the unaccounted part counts as short.
			write12h := r.InputCacheWrite12hTokens
			var write30m int64
			if r.InputCacheWrite30mTokens != nil {
				write30m = *r.InputCacheWrite30mTokens
			} else {
				write30m = clamp0(r.InputCacheWriteTokens - write12h)
			}
			rec := model.UsageRecord{
				Source:             model.SourceOpenAIAPI,
				ExternalID:         externalID(start, deref(r.Model), deref(r.ProjectID)),
				DeviceID:           deviceID,
				Timestamp:          start,
				Service:            model.ServiceOpenAI,
				Model:              deref(r.Model),
				Project:            deref(r.ProjectID),
				InputTokens:        clamp0(r.InputTokens - r.InputCachedTokens - write30m - write12h),
				OutputTokens:       r.OutputTokens,
				CacheReadTokens:    r.InputCachedTokens,
				CacheWrite5mTokens: write30m,
				CacheWrite1hTokens: write12h,
				RequestCount:       r.NumModelRequests,
			}
			if rec.TotalTokens() == 0 && rec.RequestCount == 0 {
				continue
			}
			k := recordKey{b.StartTime, rec.Model, rec.Project}
			if i, dup := index[k]; dup {
				// Defensive: the API returned two rows for the same key.
				mergeTokens(&out[i], rec)
				continue
			}
			index[k] = len(out)
			out = append(out, rec)
			keys = append(keys, k)
		}
	}

	sums := map[recordKey]float64{}
	var order []recordKey
	if costsOK {
		for _, b := range costs {
			for _, r := range b.Results {
				if !strings.EqualFold(r.Amount.Currency, "usd") || r.Amount.Value < 0 {
					continue
				}
				k := recordKey{b.StartTime, lineItemModel(deref(r.LineItem)), deref(r.ProjectID)}
				if _, ok := sums[k]; !ok {
					order = append(order, k)
				}
				sums[k] += r.Amount.Value
			}
		}
	}

	// Usage records of this fetch: fresh cost, or the one already stored.
	for i := range out {
		if usd, ok := sums[keys[i]]; ok {
			v := usd
			out[i].ReportedCostUSD = &v
			continue
		}
		stored, found, err := lookup(out[i].ExternalID)
		if err != nil {
			return nil, err
		}
		if found && stored.ReportedCostUSD != nil {
			v := *stored.ReportedCostUSD
			out[i].ReportedCostUSD = &v
		}
	}

	// Cost keys: reset a superseded cost-only record, attach to a stored
	// usage record, or create a cost-only record.
	for _, k := range order {
		v := sums[k]
		usageID := k.id()
		_, inFetch := index[k]
		hasUsage := inFetch
		if !inFetch {
			stored, found, err := lookup(usageID)
			if err != nil {
				return nil, err
			}
			if found {
				hasUsage = true
				stored.ReportedCostUSD = &v
				out = append(out, stored)
			}
		}
		stale, foundStale, err := lookup(costOnlyID(usageID))
		if err != nil {
			return nil, err
		}
		if hasUsage {
			if foundStale && stale.ReportedCostUSD != nil {
				stale.ReportedCostUSD = nil // cost now lives on the usage record
				out = append(out, stale)
			}
			continue
		}
		start := time.Unix(k.start, 0).UTC()
		out = append(out, model.UsageRecord{
			Source:          model.SourceOpenAIAPI,
			ExternalID:      costOnlyID(usageID),
			DeviceID:        deviceID,
			Timestamp:       start,
			Service:         model.ServiceOpenAI,
			Model:           k.model,
			Project:         k.project,
			ReportedCostUSD: &v,
		})
	}
	return out, nil
}

func mergeTokens(dst *model.UsageRecord, add model.UsageRecord) {
	dst.InputTokens += add.InputTokens
	dst.OutputTokens += add.OutputTokens
	dst.CacheReadTokens += add.CacheReadTokens
	dst.CacheWrite5mTokens += add.CacheWrite5mTokens
	dst.CacheWrite1hTokens += add.CacheWrite1hTokens
	dst.RequestCount += add.RequestCount
}
