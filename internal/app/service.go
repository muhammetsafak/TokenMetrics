// Package app is the binding layer between the Wails frontend and the backend
// packages. It wires the store, the collectors, the price table and the limit
// calculator into one Service and shapes their results into DTOs. It adds no
// business rules of its own: costs come from internal/pricing, windows from
// internal/limits, aggregation from internal/store.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/collector"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/anthropicapi"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/claudecode"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/codex"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/gemini"
	"github.com/muhammetsafak/TokenMetrics/internal/collector/openaiapi"
	"github.com/muhammetsafak/TokenMetrics/internal/device"
	"github.com/muhammetsafak/TokenMetrics/internal/limits"
	"github.com/muhammetsafak/TokenMetrics/internal/model"
	"github.com/muhammetsafak/TokenMetrics/internal/paths"
	"github.com/muhammetsafak/TokenMetrics/internal/pricing"
	"github.com/muhammetsafak/TokenMetrics/internal/secrets"
	"github.com/muhammetsafak/TokenMetrics/internal/store"
)

const (
	// SettingLastScan holds the unix-millisecond time the last scan finished.
	SettingLastScan = "app.last_scan_ms"

	// Step statuses of a scan.
	StatusRunning = "running"
	StatusDone    = "done"
	StatusSkipped = "skipped"
	StatusError   = "error"

	maxWarnings = 20
	// stackTop is how many stack layers a time series keeps before it folds
	// the rest into "other".
	stackTop = 6
	// maxFillBuckets bounds the gap filling of a time series.
	maxFillBuckets = 5000

	defaultSessionLimit = 100
	maxSessionLimit     = 1000
)

// maxCostRows bounds the per-row cost queries of a breakdown. A variable so
// tests can shrink it.
var maxCostRows = 300

// OtherKey is the series key of the layer that folds the smaller values.
const OtherKey = "__other__"

// ErrScanRunning is returned by Rescan while another scan is in progress.
var ErrScanRunning = errors.New("app: a scan is already running")

// sourceInfo is the static description of a source.
type sourceInfo struct {
	id       model.Source
	label    string
	kind     string
	provider string // keychain provider of an API source
}

var sources = []sourceInfo{
	{id: model.SourceClaudeCode, label: "Claude Code", kind: "local"},
	{id: model.SourceCodexCLI, label: "Codex CLI", kind: "local"},
	{id: model.SourceGeminiCLI, label: "Gemini CLI", kind: "local"},
	{id: model.SourceAnthropicAPI, label: "Anthropic API", kind: "api", provider: secrets.ProviderAnthropic},
	{id: model.SourceOpenAIAPI, label: "OpenAI API", kind: "api", provider: secrets.ProviderOpenAI},
	{id: model.SourceManual, label: "Elle giriş", kind: "manual"},
	{id: model.SourceCSVImport, label: "CSV içe aktarım", kind: "manual"},
}

func lookupSource(id model.Source) sourceInfo {
	for _, s := range sources {
		if s.id == id {
			return s
		}
	}
	return sourceInfo{id: id, label: string(id)}
}

// Config configures a Service. Only Store and Registry are required.
type Config struct {
	Store    *store.Store
	DeviceID string
	Registry *collector.Registry
	Logger   *slog.Logger
	// Now and Location default to time.Now and time.Local.
	Now      func() time.Time
	Location *time.Location
	// HasKey reports whether an Admin API key is stored for a provider; it
	// defaults to a lookup in Keys.
	HasKey func(provider string) (bool, error)
	// Keys is the Admin key storage; it defaults to the OS keychain.
	Keys KeyStore
}

// Service is the application service the Wails bindings call. It is safe for
// concurrent use.
type Service struct {
	st       *store.Store
	deviceID string
	registry *collector.Registry
	log      *slog.Logger
	now      func() time.Time
	loc      *time.Location
	hasKey   func(string) (bool, error)
	keys     KeyStore

	closeStore bool

	mu            sync.Mutex
	scanning      bool
	scanSeq       int64                // id of the newest scan
	currentID     int64                // id of the running scan, 0 when idle
	last          *ScanSummaryDTO      // newest finished scan
	observer      func(ScanSummaryDTO) // see SetScanObserver
	partial       bool                 // the running scan covers only some sources
	fullQueued    bool                 // see RunQueuedFullScan
	onBudgetState func()               // see SetBudgetStateObserver
}

// New builds a Service from cfg.
func New(cfg Config) (*Service, error) {
	if cfg.Store == nil {
		return nil, errors.New("app: store is nil")
	}
	if cfg.Registry == nil {
		return nil, errors.New("app: registry is nil")
	}
	s := &Service{
		st:       cfg.Store,
		deviceID: cfg.DeviceID,
		registry: cfg.Registry,
		log:      cfg.Logger,
		now:      cfg.Now,
		loc:      cfg.Location,
		hasKey:   cfg.HasKey,
		keys:     cfg.Keys,
	}
	if s.log == nil {
		s.log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if s.now == nil {
		s.now = time.Now
	}
	if s.loc == nil {
		s.loc = time.Local
	}
	if s.keys == nil {
		s.keys = keychain{}
	}
	if s.hasKey == nil {
		s.hasKey = s.keyConfigured
	}
	return s, nil
}

// DefaultRegistry registers every collector that exists. API collectors are
// passive until their Admin key is configured (Rescan skips them).
func DefaultRegistry() (*collector.Registry, error) {
	r := collector.NewRegistry()
	for _, c := range []collector.Collector{
		claudecode.New(claudecode.Options{}),
		codex.New(codex.Options{}),
		gemini.New(gemini.Options{}),
		anthropicapi.New(),
		openaiapi.New(),
	} {
		if err := r.Register(c); err != nil {
			return nil, err
		}
	}
	return r, nil
}

// Open starts the real service: it opens the database in the user data
// directory, loads the device id and registers the collectors.
func Open(ctx context.Context, logger *slog.Logger) (*Service, error) {
	dir, err := paths.DataDir()
	if err != nil {
		return nil, err
	}
	deviceID, err := device.LoadOrCreate(dir)
	if err != nil {
		return nil, err
	}
	dbPath, err := paths.DBPath()
	if err != nil {
		return nil, err
	}
	st, err := store.Open(ctx, dbPath)
	if err != nil {
		return nil, err
	}
	reg, err := DefaultRegistry()
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	s, err := New(Config{Store: st, DeviceID: deviceID, Registry: reg, Logger: logger})
	if err != nil {
		_ = st.Close()
		return nil, err
	}
	s.closeStore = true
	return s, nil
}

// Close closes the database when the service opened it. A running scan must
// have been canceled and waited for by the caller.
func (s *Service) Close() error {
	if !s.closeStore {
		return nil
	}
	return s.st.Close()
}

// Scanning reports whether a scan is running.
func (s *Service) Scanning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scanning
}

// scanState returns a consistent snapshot of the scan state.
func (s *Service) scanState() (scanning bool, id int64, last *ScanSummaryDTO) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.last != nil {
		cp := *s.last
		last = &cp
	}
	// A background scan of some sources is not reported as running: it ends
	// without an event when nothing changed, so a client that saw it as
	// running would wait for an end that never comes.
	if s.partial {
		return false, 0, last
	}
	return s.scanning, s.currentID, last
}

// ---- scanning -------------------------------------------------------------

func (s *Service) sourceEnabled(ctx context.Context, id model.Source) (bool, error) {
	enabled := true
	if _, err := s.st.GetSetting(ctx, "sources."+string(id)+".enabled", &enabled); err != nil {
		return false, err
	}
	return enabled, nil
}

// sourceActive reports whether a collector will run: enabled, and for an API
// source a key is stored. Keychain trouble counts as "no key" here; the
// collector would fail the same way.
func (s *Service) sourceActive(ctx context.Context, info sourceInfo) (active, enabled bool, err error) {
	enabled, err = s.sourceEnabled(ctx, info.id)
	if err != nil || !enabled {
		return false, enabled, err
	}
	if info.provider != "" {
		ok, kerr := s.hasKey(info.provider)
		if kerr != nil || !ok {
			return false, enabled, nil
		}
	}
	return true, enabled, nil
}

// BeginScan reserves the scan slot. ok is false while another scan runs; id
// is then that scan's id. Every successful call must be followed by RunScan
// with the returned id.
func (s *Service) BeginScan() (id int64, ok bool) { return s.beginScan(false) }

func (s *Service) beginScan(partial bool) (id int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.scanning {
		return s.currentID, false
	}
	s.scanSeq++
	s.currentID = s.scanSeq
	s.scanning = true
	s.partial = partial
	return s.currentID, true
}

// Rescan runs a scan synchronously (BeginScan + RunScan). It returns
// ErrScanRunning while another scan is in progress.
func (s *Service) Rescan(ctx context.Context, onStep func(ScanStep)) (ScanSummaryDTO, error) {
	id, ok := s.BeginScan()
	if !ok {
		return ScanSummaryDTO{}, ErrScanRunning
	}
	return s.RunScan(ctx, id, onStep), nil
}

// RunScan runs every active collector in registration order for the scan id
// returned by BeginScan and returns the summary. onStep (may be nil) is called
// when a collector starts and when it ends. A failing collector does not stop
// the others. When RunScan returns, Meta already reports the scan as finished
// (Scanning false, LastScan set), so a state read before this point can be
// recognized as stale by its scan id.
func (s *Service) RunScan(ctx context.Context, id int64, onStep func(ScanStep)) ScanSummaryDTO {
	return s.RunScanSources(ctx, id, nil, onStep)
}

// RunScanSources is RunScan limited to the given sources (nil: every
// source); the background refresh uses it to run one collector. The summary
// keeps the steps of the sources that were not part of this run from the
// previous scan, so the status line does not lose their warnings.
func (s *Service) RunScanSources(ctx context.Context, id int64, only []model.Source, onStep func(ScanStep)) ScanSummaryDTO {
	sum := s.runScan(ctx, id, only, onStep)
	s.mu.Lock()
	if only != nil {
		sum.Steps = mergeSteps(s.last, sum.Steps, s.registry.All())
	}
	last := sum
	s.last = &last
	s.scanning = false
	s.partial = false
	s.currentID = 0
	observer := s.observer
	s.mu.Unlock()
	if observer != nil && !sum.Canceled {
		observer(sum)
	}
	return sum
}

func (s *Service) runScan(ctx context.Context, id int64, only []model.Source, onStep func(ScanStep)) ScanSummaryDTO {
	emit := func(st ScanStep) {
		if onStep != nil {
			onStep(st)
		}
	}
	sum := ScanSummaryDTO{ID: id, StartedMs: s.now().UnixMilli(), Steps: []ScanStep{}}
	all := s.registry.All()
	if only != nil {
		all = filterCollectors(all, only)
	}
	for i, c := range all {
		if ctx.Err() != nil {
			sum.Canceled = true
			break
		}
		info := lookupSource(c.Name())
		step := ScanStep{ScanID: id, Source: string(info.id), Label: info.label, Index: i + 1, Total: len(all), Warnings: []string{}}

		active, _, err := s.sourceActive(ctx, info)
		if err != nil {
			step.Status, step.Error = StatusError, err.Error()
			sum.Steps = append(sum.Steps, step)
			emit(step)
			continue
		}
		if !active {
			step.Status = StatusSkipped
			sum.Steps = append(sum.Steps, step)
			emit(step)
			continue
		}

		step.Status = StatusRunning
		emit(step)
		res, cerr := s.collect(ctx, c)
		step.Added, step.Updated, step.Files = res.Added, res.Updated, res.Files
		step.Warnings = capWarnings(res.Warnings)
		switch {
		case cerr != nil && ctx.Err() != nil:
			step.Status = StatusError
			step.Error = "iptal edildi"
			sum.Canceled = true
		case cerr != nil:
			step.Status, step.Error = StatusError, cerr.Error()
			s.log.Warn("collector failed", "source", info.id, "error", cerr)
		default:
			step.Status = StatusDone
		}
		sum.Added += res.Added
		sum.Updated += res.Updated
		sum.Steps = append(sum.Steps, step)
		emit(step)
	}
	sum.FinishedMs = s.now().UnixMilli()
	if !sum.Canceled {
		// Best effort: a failed bookkeeping write must not fail the scan.
		if err := s.st.SetSetting(ctx, SettingLastScan, sum.FinishedMs); err != nil {
			s.log.Warn("save last scan time", "error", err)
		}
	}
	return sum
}

// collect runs one collector and turns a panic into an error so a parser bug
// cannot take the application down.
func (s *Service) collect(ctx context.Context, c collector.Collector) (res collector.Result, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("collector %s crashed: %v", c.Name(), r)
		}
	}()
	return c.Collect(ctx, collector.Deps{Store: s.st, DeviceID: s.deviceID, Logger: s.log})
}

func capWarnings(w []string) []string {
	if len(w) > maxWarnings {
		w = w[:maxWarnings]
	}
	out := make([]string, len(w))
	copy(out, w)
	return out
}

// ---- helpers --------------------------------------------------------------

func (s *Service) pricer(ctx context.Context) (*pricing.Pricer, error) {
	return pricing.Load(ctx, s.st)
}

func toStoreFilter(f FilterDTO) (store.Filter, error) {
	out := store.Filter{
		Services: append([]string(nil), f.Services...),
		Models:   append([]string(nil), f.Models...),
		Projects: append([]string(nil), f.Projects...),
		Sessions: append([]string(nil), f.Sessions...),
	}
	if f.FromMs != 0 {
		out.From = time.UnixMilli(f.FromMs)
	}
	if f.ToMs != 0 {
		out.To = time.UnixMilli(f.ToMs)
	}
	if f.FromMs != 0 && f.ToMs != 0 && f.ToMs <= f.FromMs {
		return store.Filter{}, errors.New("app: filter range is empty (to <= from)")
	}
	for _, src := range f.Sources {
		m := model.Source(src)
		if !m.Valid() {
			return store.Filter{}, fmt.Errorf("app: unknown source %q", src)
		}
		out.Sources = append(out.Sources, m)
	}
	return out, nil
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func totalsDTO(t store.Totals, c *pricing.Summary) TotalsDTO {
	d := TotalsDTO{
		Records:             t.RecordCount,
		Requests:            t.RequestCount,
		InputTokens:         t.InputTokens,
		OutputTokens:        t.OutputTokens,
		CacheReadTokens:     t.CacheReadTokens,
		CacheWrite5mTokens:  t.CacheWrite5mTokens,
		CacheWrite1hTokens:  t.CacheWrite1hTokens,
		CacheWriteTokens:    t.CacheWrite5mTokens + t.CacheWrite1hTokens,
		ThinkingTokens:      t.ThinkingTokens,
		TotalTokens:         t.TotalTokens(),
		TotalOnlyTokens:     t.TotalOnlyTokens,
		TotalOnlyRecords:    t.TotalOnlyRecords,
		UnknownModels:       []string{},
		TierUncertainModels: []string{},
	}
	if c != nil {
		usd := c.USD
		d.CostUSD = &usd
		d.CostEstimated = c.Estimated
		d.UnknownModels = nonNil(c.UnknownModels)
		d.TierUncertainModels = nonNil(c.TierUncertainModels)
		d.UncostedTotalOnlyTokens = c.TotalOnlyTokens
		if c.TotalOnlyRecords > 0 {
			if c.TotalOnlyRecords >= t.RecordCount {
				// Nothing in the set can be priced: not computable, not 0.
				d.CostUSD = nil
				d.CostUnavailable = true
			} else {
				d.CostPartial = true
			}
		}
	}
	return d
}

// costOf prices the records matching f.
func (s *Service) costOf(ctx context.Context, p *pricing.Pricer, f store.Filter) (pricing.Summary, error) {
	in, err := p.CostInputs(ctx, s.st, f)
	if err != nil {
		return pricing.Summary{}, err
	}
	return p.Summarize(in), nil
}

// totals returns the totals and the cost of the records matching f.
func (s *Service) totals(ctx context.Context, p *pricing.Pricer, f store.Filter) (TotalsDTO, error) {
	t, err := s.st.Totals(ctx, f)
	if err != nil {
		return TotalsDTO{}, err
	}
	c, err := s.costOf(ctx, p, f)
	if err != nil {
		return TotalsDTO{}, err
	}
	return totalsDTO(t, &c), nil
}

// narrow returns f restricted to a single value of dim (replacing that
// dimension's list). ok is false for dimensions the store cannot filter by.
func narrow(f store.Filter, dim store.Dimension, key string) (store.Filter, bool) {
	switch dim {
	case store.DimService:
		f.Services = []string{key}
	case store.DimModel:
		f.Models = []string{key}
	case store.DimProject:
		f.Projects = []string{key}
	case store.DimSession:
		f.Sessions = []string{key}
	case store.DimSource:
		f.Sources = []model.Source{model.Source(key)}
	default:
		return f, false
	}
	return f, true
}

func localMidnight(t time.Time, loc *time.Location, addDays int) time.Time {
	y, m, d := t.In(loc).Date()
	return time.Date(y, m, d+addDays, 0, 0, 0, 0, loc)
}

// ---- queries --------------------------------------------------------------

// Summary returns the headline totals: today, the last 7 and 30 local days,
// and the selected range.
func (s *Service) Summary(ctx context.Context, f FilterDTO) (SummaryDTO, error) {
	base, err := toStoreFilter(f)
	if err != nil {
		return SummaryDTO{}, err
	}
	p, err := s.pricer(ctx)
	if err != nil {
		return SummaryDTO{}, err
	}
	now := s.now()
	tomorrow := localMidnight(now, s.loc, 1)
	out := SummaryDTO{
		PriceAsOf:  p.AsOf(),
		NowMs:      now.UnixMilli(),
		TimeZone:   s.loc.String(),
		DayStartMs: localMidnight(now, s.loc, 0).UnixMilli(),
	}
	fixed := base
	run := func(dst *TotalsDTO, from time.Time) error {
		fixed.From, fixed.To = from, tomorrow
		var e error
		*dst, e = s.totals(ctx, p, fixed)
		return e
	}
	if err := run(&out.Today, localMidnight(now, s.loc, 0)); err != nil {
		return SummaryDTO{}, err
	}
	if err := run(&out.Last7d, localMidnight(now, s.loc, -6)); err != nil {
		return SummaryDTO{}, err
	}
	if err := run(&out.Last30d, localMidnight(now, s.loc, -29)); err != nil {
		return SummaryDTO{}, err
	}
	if out.Selected, err = s.totals(ctx, p, base); err != nil {
		return SummaryDTO{}, err
	}
	return out, nil
}

func parseBucket(b string) (store.Bucket, error) {
	switch store.Bucket(b) {
	case store.BucketHour, store.BucketDay, store.BucketWeek, store.BucketMonth:
		return store.Bucket(b), nil
	}
	return "", fmt.Errorf("app: unknown bucket %q", b)
}

// alignBucket returns the start of the bucket that contains t. It mirrors the
// store's bucket alignment so empty buckets can be filled in.
func alignBucket(t time.Time, b store.Bucket, loc *time.Location) time.Time {
	t = t.In(loc).Truncate(time.Minute)
	y, m, d := t.Date()
	switch b {
	case store.BucketHour:
		return t.Add(-time.Duration(t.Minute()) * time.Minute)
	case store.BucketDay:
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	case store.BucketWeek:
		back := (int(t.Weekday()) + 6) % 7
		return time.Date(y, m, d-back, 0, 0, 0, 0, loc)
	default:
		return time.Date(y, m, 1, 0, 0, 0, 0, loc)
	}
}

func nextBucket(t time.Time, b store.Bucket, loc *time.Location) time.Time {
	y, m, d := t.In(loc).Date()
	switch b {
	case store.BucketHour:
		return t.Add(time.Hour)
	case store.BucketDay:
		return time.Date(y, m, d+1, 0, 0, 0, 0, loc)
	case store.BucketWeek:
		return time.Date(y, m, d+7, 0, 0, 0, 0, loc)
	default:
		return time.Date(y, m+1, 1, 0, 0, 0, 0, loc)
	}
}

func parseStackBy(stackBy string) (store.Dimension, error) {
	switch store.Dimension(stackBy) {
	case "":
		return "", nil
	case store.DimService, store.DimModel, store.DimSource:
		return store.Dimension(stackBy), nil
	}
	return "", fmt.Errorf("app: cannot stack by %q", stackBy)
}

// TimeSeries returns total tokens per bucket. stackBy ("", service, model or
// source) splits each bucket into the stackTop largest values plus an "other"
// layer. Empty buckets inside the range are filled with zeros.
func (s *Service) TimeSeries(ctx context.Context, f FilterDTO, bucket, stackBy string) (TimeSeriesDTO, error) {
	base, err := toStoreFilter(f)
	if err != nil {
		return TimeSeriesDTO{}, err
	}
	b, err := parseBucket(bucket)
	if err != nil {
		return TimeSeriesDTO{}, err
	}
	dim, err := parseStackBy(stackBy)
	if err != nil {
		return TimeSeriesDTO{}, err
	}

	totalPts, err := s.st.TimeSeries(ctx, base, b, s.loc)
	if err != nil {
		return TimeSeriesDTO{}, err
	}

	type layer struct {
		key string
		pts []store.TimeSeriesPoint
	}
	var layers []layer
	keys := []string(nil)
	if dim != "" {
		rows, err := s.st.Breakdown(ctx, base, dim)
		if err != nil {
			return TimeSeriesDTO{}, err
		}
		for i, r := range rows {
			if i >= stackTop {
				break
			}
			keys = append(keys, r.Key)
		}
		for _, k := range keys {
			nf, _ := narrow(base, dim, k)
			pts, err := s.st.TimeSeries(ctx, nf, b, s.loc)
			if err != nil {
				return TimeSeriesDTO{}, err
			}
			layers = append(layers, layer{key: k, pts: pts})
		}
	}

	// Bucket axis: every point of the total series, plus the empty buckets
	// between the range start and now.
	starts := map[int64]time.Time{}
	for _, p := range totalPts {
		starts[p.Start.UnixMilli()] = p.Start
	}
	var from time.Time
	switch {
	case !base.From.IsZero():
		from = alignBucket(base.From, b, s.loc)
	case len(totalPts) > 0:
		from = totalPts[0].Start
	}
	if !from.IsZero() {
		end := s.now().Add(time.Millisecond)
		if !base.To.IsZero() && base.To.Before(end) {
			end = base.To
		}
		for t, n := from, 0; t.Before(end) && n < maxFillBuckets; t, n = nextBucket(t, b, s.loc), n+1 {
			starts[t.UnixMilli()] = t
		}
	}
	order := make([]int64, 0, len(starts))
	for k := range starts {
		order = append(order, k)
	}
	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })

	values := func(pts []store.TimeSeriesPoint) []int64 {
		byStart := make(map[int64]int64, len(pts))
		for _, p := range pts {
			byStart[p.Start.UnixMilli()] = p.TotalTokens()
		}
		out := make([]int64, len(order))
		for i, k := range order {
			out[i] = byStart[k]
		}
		return out
	}

	res := TimeSeriesDTO{
		Bucket:  bucket,
		StackBy: stackBy,
		Buckets: order,
		Series:  []SeriesDTO{},
	}
	total := values(totalPts)
	if dim == "" {
		res.Series = append(res.Series, SeriesDTO{Key: "", Values: total})
		return res, nil
	}
	rest := append([]int64(nil), total...)
	for _, l := range layers {
		v := values(l.pts)
		for i := range rest {
			rest[i] -= v[i]
		}
		res.Series = append(res.Series, SeriesDTO{Key: l.key, Values: v})
	}
	hasRest := false
	for i := range rest {
		if rest[i] < 0 {
			rest[i] = 0
		}
		if rest[i] > 0 {
			hasRest = true
		}
	}
	if hasRest {
		res.OtherKey = OtherKey
		res.Series = append(res.Series, SeriesDTO{Key: OtherKey, Values: rest})
	}
	return res, nil
}

func parseDimension(dim string) (store.Dimension, error) {
	switch store.Dimension(dim) {
	case store.DimService, store.DimModel, store.DimProject, store.DimSession, store.DimSource:
		return store.Dimension(dim), nil
	}
	return "", fmt.Errorf("app: unknown dimension %q", dim)
}

// breakdown builds the rows of a breakdown with costs. limit > 0 keeps only
// the largest limit rows (truncated reports that rows were dropped). Costs are
// per-row queries (one shared query for the model dimension), capped at
// maxCostRows rows; costTruncated reports that rows beyond the cap have no
// cost.
func (s *Service) breakdown(ctx context.Context, p *pricing.Pricer, f store.Filter, dim store.Dimension, limit int) (out []BreakdownRowDTO, truncated, costTruncated bool, err error) {
	rows, err := s.st.Breakdown(ctx, f, dim)
	if err != nil {
		return nil, false, false, err
	}
	if limit > 0 && len(rows) > limit {
		rows, truncated = rows[:limit], true
	}
	out = make([]BreakdownRowDTO, 0, len(rows))

	var byModel map[string]store.CostInput
	if dim == store.DimModel {
		in, err := p.CostInputs(ctx, s.st, f)
		if err != nil {
			return nil, false, false, err
		}
		byModel = make(map[string]store.CostInput, len(in))
		for _, ci := range in {
			byModel[ci.Model] = ci
		}
	}

	for i, r := range rows {
		var c *pricing.Summary
		switch {
		case dim == store.DimModel:
			sum := p.Summarize([]store.CostInput{byModel[r.Key]})
			c = &sum
		case i < maxCostRows:
			nf, _ := narrow(f, dim, r.Key)
			sum, err := s.costOf(ctx, p, nf)
			if err != nil {
				return nil, false, false, err
			}
			c = &sum
		default:
			costTruncated = true
		}
		out = append(out, BreakdownRowDTO{Key: r.Key, Totals: totalsDTO(r.Totals, c)})
	}
	return out, truncated, costTruncated, nil
}

// Breakdown returns the totals per value of dim (service, model, project,
// session or source), largest first.
func (s *Service) Breakdown(ctx context.Context, f FilterDTO, dim string) (BreakdownDTO, error) {
	base, err := toStoreFilter(f)
	if err != nil {
		return BreakdownDTO{}, err
	}
	d, err := parseDimension(dim)
	if err != nil {
		return BreakdownDTO{}, err
	}
	p, err := s.pricer(ctx)
	if err != nil {
		return BreakdownDTO{}, err
	}
	rows, _, costTrunc, err := s.breakdown(ctx, p, base, d, 0)
	if err != nil {
		return BreakdownDTO{}, err
	}
	return BreakdownDTO{Dimension: dim, Rows: rows, CostTruncated: costTrunc}, nil
}

// Sessions returns the largest sessions matching the filter (limit <= 0 means
// 100, at most 1000), each with its estimated cost.
func (s *Service) Sessions(ctx context.Context, f FilterDTO, limit int) (BreakdownDTO, error) {
	if limit <= 0 {
		limit = defaultSessionLimit
	}
	if limit > maxSessionLimit {
		limit = maxSessionLimit
	}
	base, err := toStoreFilter(f)
	if err != nil {
		return BreakdownDTO{}, err
	}
	p, err := s.pricer(ctx)
	if err != nil {
		return BreakdownDTO{}, err
	}
	rows, truncated, costTrunc, err := s.breakdown(ctx, p, base, store.DimSession, limit)
	if err != nil {
		return BreakdownDTO{}, err
	}
	return BreakdownDTO{Dimension: string(store.DimSession), Rows: rows, Truncated: truncated, CostTruncated: costTrunc}, nil
}

// Compare returns one row per service x model combination.
func (s *Service) Compare(ctx context.Context, f FilterDTO) (CompareDTO, error) {
	base, err := toStoreFilter(f)
	if err != nil {
		return CompareDTO{}, err
	}
	p, err := s.pricer(ctx)
	if err != nil {
		return CompareDTO{}, err
	}
	services, err := s.st.Breakdown(ctx, base, store.DimService)
	if err != nil {
		return CompareDTO{}, err
	}
	res := CompareDTO{Rows: []CompareRowDTO{}, PriceAsOf: p.AsOf()}
	for _, svc := range services {
		sf, _ := narrow(base, store.DimService, svc.Key)
		rows, _, _, err := s.breakdown(ctx, p, sf, store.DimModel, 0)
		if err != nil {
			return CompareDTO{}, err
		}
		for _, r := range rows {
			res.Rows = append(res.Rows, CompareRowDTO{Service: svc.Key, Model: r.Key, Totals: r.Totals})
		}
	}
	return res, nil
}

// Limits evaluates the configured limit windows and reports the usage inside
// the current 5h and weekly windows.
func (s *Service) Limits(ctx context.Context) (LimitsDTO, error) {
	p, err := s.pricer(ctx)
	if err != nil {
		return LimitsDTO{}, err
	}
	now := s.now()
	calc := limits.New(s.st, p)
	results, err := calc.EvaluateAll(ctx, now)
	if err != nil {
		return LimitsDTO{}, err
	}
	out := LimitsDTO{
		Limits:    make([]LimitDTO, 0, len(results)),
		Current:   []CurrentWindowDTO{},
		PriceAsOf: p.AsOf(),
		NowMs:     now.UnixMilli(),
	}
	for _, r := range results {
		out.Limits = append(out.Limits, LimitDTO{
			Service:             r.Service,
			Kind:                r.Kind,
			Metric:              r.Metric,
			Used:                r.Used,
			Limit:               r.Limit,
			Ratio:               r.Ratio,
			WindowStartMs:       r.WindowStart.UnixMilli(),
			WindowEndMs:         r.WindowEnd.UnixMilli(),
			Estimated:           r.Estimated,
			UnknownModels:       nonNil(r.UnknownModels),
			TierUncertainModels: nonNil(r.TierUncertainModels),

			MissingTotalOnlyTokens: r.MissingTotalOnlyTokens,
		})
	}

	fiveStart, fiveEnd := limits.FiveHourWindow(now)
	cfg, err := calc.WeeklyStartSetting(ctx)
	if err != nil {
		return LimitsDTO{}, err
	}
	weekStart, weekEnd, err := limits.WeeklyWindow(now, cfg)
	if err != nil {
		return LimitsDTO{}, err
	}
	for _, w := range []struct {
		kind       string
		start, end time.Time
	}{
		{store.LimitKind5h, fiveStart, fiveEnd},
		{store.LimitKindWeekly, weekStart, weekEnd},
	} {
		t, err := s.totals(ctx, p, store.Filter{From: w.start, To: now.Add(time.Millisecond)})
		if err != nil {
			return LimitsDTO{}, err
		}
		out.Current = append(out.Current, CurrentWindowDTO{
			Kind: w.kind, StartMs: w.start.UnixMilli(), EndMs: w.end.UnixMilli(), Totals: t,
		})
	}
	return out, nil
}

// Meta lists the values the filters can choose from and the scan state.
func (s *Service) Meta(ctx context.Context) (MetaDTO, error) {
	p, err := s.pricer(ctx)
	if err != nil {
		return MetaDTO{}, err
	}
	m := MetaDTO{
		Sources:   []SourceDTO{},
		Services:  []string{},
		Models:    []string{},
		Projects:  []string{},
		PriceAsOf: p.AsOf(),
		TimeZone:  s.loc.String(),
	}
	keys := func(dim store.Dimension) ([]string, error) {
		rows, err := s.st.Breakdown(ctx, store.Filter{}, dim)
		if err != nil {
			return nil, err
		}
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.Key)
		}
		return out, nil
	}
	if m.Services, err = keys(store.DimService); err != nil {
		return MetaDTO{}, err
	}
	if m.Models, err = keys(store.DimModel); err != nil {
		return MetaDTO{}, err
	}
	if m.Projects, err = keys(store.DimProject); err != nil {
		return MetaDTO{}, err
	}

	counts := map[string]int64{}
	srcRows, err := s.st.Breakdown(ctx, store.Filter{}, store.DimSource)
	if err != nil {
		return MetaDTO{}, err
	}
	for _, r := range srcRows {
		counts[r.Key] = r.RecordCount
		m.TotalRecords += r.RecordCount
	}
	for _, info := range sources {
		_, hasCollector := s.registry.Get(info.id)
		dto := SourceDTO{
			ID:        string(info.id),
			Label:     info.label,
			Kind:      info.kind,
			Collector: hasCollector,
			Records:   counts[string(info.id)],
		}
		if hasCollector {
			active, enabled, err := s.sourceActive(ctx, info)
			if err != nil {
				return MetaDTO{}, err
			}
			dto.Active, dto.Enabled = active, enabled
		} else {
			dto.Enabled = true
		}
		m.Sources = append(m.Sources, dto)
	}

	if m.TotalRecords > 0 {
		pts, err := s.st.TimeSeries(ctx, store.Filter{}, store.BucketMonth, s.loc)
		if err != nil {
			return MetaDTO{}, err
		}
		if len(pts) > 0 {
			m.FirstRecordMs = pts[0].Start.UnixMilli()
		}
	}

	var last int64
	if _, err := s.st.GetSetting(ctx, SettingLastScan, &last); err != nil {
		return MetaDTO{}, err
	}
	m.LastScanMs = last
	// Read the scan state last: it is the freshest part of the snapshot, and
	// the scan ids let the caller discard whichever of Meta and the scan
	// events is older.
	m.Scanning, m.ScanID, m.LastScan = s.scanState()
	return m, nil
}
