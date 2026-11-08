package app

// DTOs are the only types the Wails bindings return or accept. Times are unix
// milliseconds (UTC instants; the frontend renders them in the local zone).
// JSON keys are camelCase because they surface as TypeScript properties.

// FilterDTO is the common filter of every query. Empty lists mean "no filter";
// a zero FromMs or ToMs leaves that side of the range open (FromMs inclusive,
// ToMs exclusive).
type FilterDTO struct {
	FromMs   int64    `json:"fromMs"`
	ToMs     int64    `json:"toMs"`
	Sources  []string `json:"sources"`
	Services []string `json:"services"`
	Models   []string `json:"models"`
	Projects []string `json:"projects"`
	Sessions []string `json:"sessions"`
}

// TotalsDTO is the sum of a set of usage records and its estimated cost.
type TotalsDTO struct {
	Records            int64 `json:"records"`
	Requests           int64 `json:"requests"`
	InputTokens        int64 `json:"inputTokens"`
	OutputTokens       int64 `json:"outputTokens"`
	CacheReadTokens    int64 `json:"cacheReadTokens"`
	CacheWrite5mTokens int64 `json:"cacheWrite5mTokens"`
	CacheWrite1hTokens int64 `json:"cacheWrite1hTokens"`
	CacheWriteTokens   int64 `json:"cacheWriteTokens"` // 5m + 1h
	ThinkingTokens     int64 `json:"thinkingTokens"`
	TotalTokens        int64 `json:"totalTokens"` // thinking not included; includes TotalOnlyTokens

	// TotalOnlyTokens counts the tokens of records that carry only a combined
	// count (no input/output split, e.g. newer Codex CLI logs); they are in
	// TotalTokens but in none of the split counters above. TotalOnlyRecords is
	// how many records that was.
	TotalOnlyTokens  int64 `json:"totalOnlyTokens"`
	TotalOnlyRecords int64 `json:"totalOnlyRecords"`

	// CostUSD is absent for rows beyond the per-row cost cap (BreakdownDTO.CostTruncated).
	CostUSD *float64 `json:"costUsd,omitempty"`
	// CostUnavailable is true when the cost could not be computed because every
	// record of the set is total-only (no split to price, see TotalOnlyTokens).
	// CostUSD is then absent; that is "not computable", not a cost of 0.
	CostUnavailable bool `json:"costUnavailable"`
	// CostPartial is true when some, but not all, records are total-only and
	// carry no reported cost: CostUSD leaves their tokens out
	// (UncostedTotalOnlyTokens) and is a lower bound.
	CostPartial             bool  `json:"costPartial"`
	UncostedTotalOnlyTokens int64 `json:"uncostedTotalOnlyTokens"`
	// CostEstimated is true when part of CostUSD comes from the price table
	// instead of being reported by the source.
	CostEstimated bool `json:"costEstimated"`
	// UnknownModels have no price; their usage is missing from CostUSD.
	UnknownModels []string `json:"unknownModels"`
	// TierUncertainModels have a long-prompt price tier and records without a
	// request count; CostUSD is a lower bound for them.
	TierUncertainModels []string `json:"tierUncertainModels"`
}

// SummaryDTO holds the headline totals. Today, Last7d and Last30d use the
// local calendar (today is local midnight to the next midnight; 7 and 30 days
// include today). They honor the filter's source, service, model and project
// lists but not its time range, which only Selected uses.
type SummaryDTO struct {
	Today      TotalsDTO `json:"today"`
	Last7d     TotalsDTO `json:"last7d"`
	Last30d    TotalsDTO `json:"last30d"`
	Selected   TotalsDTO `json:"selected"`
	PriceAsOf  string    `json:"priceAsOf"`
	NowMs      int64     `json:"nowMs"`
	TimeZone   string    `json:"timeZone"`
	DayStartMs int64     `json:"dayStartMs"` // local midnight of today
}

// SeriesDTO is one stack layer of a time series: Values[i] is the total token
// count of bucket Buckets[i].
type SeriesDTO struct {
	Key    string  `json:"key"` // "" for the unstacked total
	Values []int64 `json:"values"`
}

// TimeSeriesDTO is a gap-filled time series. Buckets are bucket start times.
type TimeSeriesDTO struct {
	Bucket  string      `json:"bucket"`  // hour | day | week | month
	StackBy string      `json:"stackBy"` // "" | service | model | source
	Buckets []int64     `json:"buckets"`
	Series  []SeriesDTO `json:"series"`
	// OtherKey is the Key of the layer that folds the smaller values of
	// StackBy together ("" when there is none).
	OtherKey string `json:"otherKey"`
}

// BreakdownRowDTO is one value of a breakdown dimension.
type BreakdownRowDTO struct {
	Key    string    `json:"key"`
	Totals TotalsDTO `json:"totals"`
}

// BreakdownDTO is the result of Breakdown and Sessions.
type BreakdownDTO struct {
	Dimension string            `json:"dimension"`
	Rows      []BreakdownRowDTO `json:"rows"`
	// Truncated is true when more rows exist than were returned.
	Truncated bool `json:"truncated"`
	// CostTruncated is true when some rows have no cost only because the
	// number of per-row cost queries is capped (CostUSD absent on those rows).
	CostTruncated bool `json:"costTruncated"`
}

// CompareRowDTO is one service x model combination.
type CompareRowDTO struct {
	Service string    `json:"service"`
	Model   string    `json:"model"`
	Totals  TotalsDTO `json:"totals"`
}

// CompareDTO is the service x model table.
type CompareDTO struct {
	Rows      []CompareRowDTO `json:"rows"`
	PriceAsOf string          `json:"priceAsOf"`
}

// LimitDTO is the consumption of one configured limit window.
type LimitDTO struct {
	Service             string   `json:"service"` // "" = all services
	Kind                string   `json:"kind"`    // 5h | weekly
	Metric              string   `json:"metric"`  // total_tokens | output_tokens | cost_usd
	Used                float64  `json:"used"`
	Limit               float64  `json:"limit"`
	Ratio               float64  `json:"ratio"`
	WindowStartMs       int64    `json:"windowStartMs"`
	WindowEndMs         int64    `json:"windowEndMs"`
	Estimated           bool     `json:"estimated"`
	UnknownModels       []string `json:"unknownModels"`
	TierUncertainModels []string `json:"tierUncertainModels"`
	// MissingTotalOnlyTokens are tokens of total-only records that Used leaves
	// out (see limits.Result); Used is a lower bound when it is above 0.
	MissingTotalOnlyTokens int64 `json:"missingTotalOnlyTokens"`
}

// CurrentWindowDTO is the usage (all services) inside the current 5h or weekly
// window, shown whether or not a limit is configured.
type CurrentWindowDTO struct {
	Kind    string    `json:"kind"` // 5h | weekly
	StartMs int64     `json:"startMs"`
	EndMs   int64     `json:"endMs"`
	Totals  TotalsDTO `json:"totals"`
}

// LimitsDTO is the data of the limits screen.
type LimitsDTO struct {
	Limits    []LimitDTO         `json:"limits"`
	Current   []CurrentWindowDTO `json:"current"`
	PriceAsOf string             `json:"priceAsOf"`
	NowMs     int64              `json:"nowMs"`
}

// SourceDTO describes one usage source.
type SourceDTO struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	// Kind is local (log files), api (Admin API) or manual.
	Kind string `json:"kind"`
	// Collector is true when a collector scans this source.
	Collector bool `json:"collector"`
	// Enabled is the user's switch (setting sources.<id>.enabled, default on).
	Enabled bool `json:"enabled"`
	// Active is true when the source will be scanned: enabled, and for an API
	// source an Admin key is configured.
	Active  bool  `json:"active"`
	Records int64 `json:"records"`
}

// MetaDTO lists what the filter bar can choose from and the scan state.
type MetaDTO struct {
	Sources      []SourceDTO `json:"sources"`
	Services     []string    `json:"services"`
	Models       []string    `json:"models"`
	Projects     []string    `json:"projects"`
	TotalRecords int64       `json:"totalRecords"`
	// FirstRecordMs is the start of the month of the oldest record (0 = none).
	FirstRecordMs int64 `json:"firstRecordMs"`
	LastScanMs    int64 `json:"lastScanMs"` // 0 = never
	Scanning      bool  `json:"scanning"`
	// ScanID is the id of the running scan (0 when none).
	ScanID int64 `json:"scanId"`
	// LastScan is the summary of the last finished scan of this run of the
	// application; absent before the first one ends.
	LastScan  *ScanSummaryDTO `json:"lastScan,omitempty"`
	PriceAsOf string          `json:"priceAsOf"`
	TimeZone  string          `json:"timeZone"`
}

// ScanStep is the state of one collector inside a scan; it is also the payload
// of the scan:progress event.
type ScanStep struct {
	ScanID  int64  `json:"scanId"`
	Source  string `json:"source"`
	Label   string `json:"label"`
	Index   int    `json:"index"` // 1-based position among the collectors
	Total   int    `json:"total"`
	Status  string `json:"status"` // running | done | skipped | error
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Files   int    `json:"files"`
	// Warnings are capped at maxWarnings per step.
	Warnings []string `json:"warnings"`
	Error    string   `json:"error"`
}

// ScanSummaryDTO is the payload of the scan:done event and the result of
// Service.Rescan.
type ScanSummaryDTO struct {
	// ID numbers the scans of this run of the application (1, 2, ...). The
	// frontend uses it to tell a stale scan state from the current one.
	ID         int64      `json:"id"`
	StartedMs  int64      `json:"startedMs"`
	FinishedMs int64      `json:"finishedMs"`
	Added      int        `json:"added"`
	Updated    int        `json:"updated"`
	Canceled   bool       `json:"canceled"`
	Steps      []ScanStep `json:"steps"`
}
