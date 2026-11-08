package store

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// Filter narrows the records an aggregation looks at. The zero value matches
// everything. Within one slice the values are OR-ed; the different fields are
// AND-ed.
type Filter struct {
	From     time.Time // inclusive; zero = unbounded
	To       time.Time // exclusive; zero = unbounded
	Sources  []model.Source
	Services []string
	Models   []string
	Projects []string
	Sessions []string
}

// Totals is the sum of a set of usage records.
type Totals struct {
	RecordCount        int64
	RequestCount       int64
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
	ThinkingTokens     int64

	// TotalOnlyTokens is the combined count of the total-only records (sources
	// that report no input/output split); TotalOnlyRecords says how many
	// records that was. Those tokens are in none of the split counters above
	// but are part of TotalTokens.
	TotalOnlyTokens  int64
	TotalOnlyRecords int64

	// ReportedCostUSD sums only the records whose source reported a cost;
	// CostReportedRecords says how many that was (out of RecordCount).
	ReportedCostUSD     float64
	CostReportedRecords int64
}

// TotalTokens returns input + output + cache read + cache write tokens plus
// the total-only tokens (thinking tokens are not added, see
// model.UsageRecord.TotalTokens).
func (t Totals) TotalTokens() int64 {
	return t.InputTokens + t.OutputTokens + t.CacheReadTokens +
		t.CacheWrite5mTokens + t.CacheWrite1hTokens + t.TotalOnlyTokens
}

func (t *Totals) add(o Totals) {
	t.RecordCount += o.RecordCount
	t.RequestCount += o.RequestCount
	t.InputTokens += o.InputTokens
	t.OutputTokens += o.OutputTokens
	t.CacheReadTokens += o.CacheReadTokens
	t.CacheWrite5mTokens += o.CacheWrite5mTokens
	t.CacheWrite1hTokens += o.CacheWrite1hTokens
	t.ThinkingTokens += o.ThinkingTokens
	t.TotalOnlyTokens += o.TotalOnlyTokens
	t.TotalOnlyRecords += o.TotalOnlyRecords
	t.ReportedCostUSD += o.ReportedCostUSD
	t.CostReportedRecords += o.CostReportedRecords
}

// Bucket is a time-series bucket size.
type Bucket string

// Supported buckets. Weeks start on Monday.
const (
	BucketHour  Bucket = "hour"
	BucketDay   Bucket = "day"
	BucketWeek  Bucket = "week"
	BucketMonth Bucket = "month"
)

// TimeSeriesPoint is the total of one time bucket.
type TimeSeriesPoint struct {
	Start time.Time // bucket start, in the location passed to TimeSeries
	Totals
}

// Dimension is a breakdown dimension.
type Dimension string

// Supported breakdown dimensions.
const (
	DimService Dimension = "service"
	DimModel   Dimension = "model"
	DimProject Dimension = "project"
	DimSession Dimension = "session"
	DimSource  Dimension = "source"
)

// BreakdownRow is the total for one value of a dimension.
type BreakdownRow struct {
	Key string // the dimension value; "" when the records carry none
	Totals
}

// dimColumns maps dimensions to column names. It is the only place a
// caller-chosen value reaches SQL text, and only through this whitelist.
var dimColumns = map[Dimension]string{
	DimService: "service",
	DimModel:   "model",
	DimProject: "project",
	DimSession: "session",
	DimSource:  "source",
}

const totalsSelect = `COUNT(*),
	COALESCE(SUM(request_count), 0),
	COALESCE(SUM(input_tokens), 0),
	COALESCE(SUM(output_tokens), 0),
	COALESCE(SUM(cache_read_tokens), 0),
	COALESCE(SUM(cache_write_5m_tokens), 0),
	COALESCE(SUM(cache_write_1h_tokens), 0),
	COALESCE(SUM(thinking_tokens), 0),
	COALESCE(SUM(reported_cost_usd), 0),
	COUNT(reported_cost_usd),
	COALESCE(SUM(total_only_tokens), 0),
	COALESCE(SUM(total_only), 0)`

type scanner interface{ Scan(dest ...any) error }

func scanTotals(sc scanner, lead ...any) (Totals, error) {
	var t Totals
	dest := append(lead,
		&t.RecordCount, &t.RequestCount, &t.InputTokens, &t.OutputTokens,
		&t.CacheReadTokens, &t.CacheWrite5mTokens, &t.CacheWrite1hTokens,
		&t.ThinkingTokens, &t.ReportedCostUSD, &t.CostReportedRecords,
		&t.TotalOnlyTokens, &t.TotalOnlyRecords)
	err := sc.Scan(dest...)
	return t, err
}

// where builds the WHERE clause (with leading space) and its arguments.
func (f Filter) where() (string, []any) {
	var conds []string
	var args []any
	if !f.From.IsZero() {
		conds = append(conds, "ts >= ?")
		args = append(args, f.From.UnixMilli())
	}
	if !f.To.IsZero() {
		conds = append(conds, "ts < ?")
		args = append(args, f.To.UnixMilli())
	}
	in := func(col string, n int, val func(i int) any) {
		if n == 0 {
			return
		}
		conds = append(conds, col+" IN ("+strings.TrimSuffix(strings.Repeat("?,", n), ",")+")")
		for i := 0; i < n; i++ {
			args = append(args, val(i))
		}
	}
	in("source", len(f.Sources), func(i int) any { return string(f.Sources[i]) })
	in("service", len(f.Services), func(i int) any { return f.Services[i] })
	in("model", len(f.Models), func(i int) any { return f.Models[i] })
	in("project", len(f.Projects), func(i int) any { return f.Projects[i] })
	in("session", len(f.Sessions), func(i int) any { return f.Sessions[i] })
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

// Totals sums all records matching the filter. With no matches every field is zero.
func (s *Store) Totals(ctx context.Context, f Filter) (Totals, error) {
	where, args := f.where()
	t, err := scanTotals(s.db.QueryRowContext(ctx,
		"SELECT "+totalsSelect+" FROM usage_records"+where, args...))
	if err != nil {
		return Totals{}, fmt.Errorf("store: totals: %w", err)
	}
	return t, nil
}

// groupMillis is the granularity SQL aggregates at before TimeSeries folds the
// groups into buckets of the caller's time zone. All real-world UTC offsets
// are multiples of 15 minutes, so every local hour/day/week/month boundary
// lies on this grid and a group never straddles two buckets.
const groupMillis = int64(15 * time.Minute / time.Millisecond)

// TimeSeries returns totals per time bucket, ordered by bucket start.
// Buckets are aligned in loc (nil means UTC), so a "day" is the local calendar
// day. Buckets without records are omitted; callers fill gaps if they need to.
func (s *Store) TimeSeries(ctx context.Context, f Filter, bucket Bucket, loc *time.Location) ([]TimeSeriesPoint, error) {
	switch bucket {
	case BucketHour, BucketDay, BucketWeek, BucketMonth:
	default:
		return nil, fmt.Errorf("store: unknown bucket %q", bucket)
	}
	if loc == nil {
		loc = time.UTC
	}

	where, args := f.where()
	rows, err := s.db.QueryContext(ctx,
		fmt.Sprintf("SELECT ts / %d AS g, %s FROM usage_records%s GROUP BY g ORDER BY g",
			groupMillis, totalsSelect, where), args...)
	if err != nil {
		return nil, fmt.Errorf("store: time series: %w", err)
	}
	defer rows.Close()

	byStart := map[int64]*TimeSeriesPoint{}
	var order []int64
	for rows.Next() {
		var g int64
		t, err := scanTotals(rows, &g)
		if err != nil {
			return nil, fmt.Errorf("store: time series: %w", err)
		}
		start := bucketStart(time.UnixMilli(g*groupMillis).In(loc), bucket)
		key := start.UnixMilli()
		p, ok := byStart[key]
		if !ok {
			p = &TimeSeriesPoint{Start: start}
			byStart[key] = p
			order = append(order, key)
		}
		p.Totals.add(t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: time series: %w", err)
	}

	sort.Slice(order, func(i, j int) bool { return order[i] < order[j] })
	out := make([]TimeSeriesPoint, 0, len(order))
	for _, k := range order {
		out = append(out, *byStart[k])
	}
	return out, nil
}

// bucketStart returns the start of the bucket containing t; t is already in
// the target location.
func bucketStart(t time.Time, b Bucket) time.Time {
	y, m, d := t.Date()
	loc := t.Location()
	switch b {
	case BucketHour:
		// Subtracting the minutes keeps the result unambiguous during a
		// DST fall-back, where the same wall-clock hour occurs twice.
		return t.Add(-time.Duration(t.Minute()) * time.Minute)
	case BucketDay:
		return time.Date(y, m, d, 0, 0, 0, 0, loc)
	case BucketWeek:
		back := (int(t.Weekday()) + 6) % 7 // Monday = 0
		return time.Date(y, m, d-back, 0, 0, 0, 0, loc)
	default: // BucketMonth
		return time.Date(y, m, 1, 0, 0, 0, 0, loc)
	}
}

// Breakdown returns totals per value of dim, largest total token count first
// (ties broken by key).
func (s *Store) Breakdown(ctx context.Context, f Filter, dim Dimension) ([]BreakdownRow, error) {
	col, ok := dimColumns[dim]
	if !ok {
		return nil, fmt.Errorf("store: unknown dimension %q", dim)
	}
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`SELECT %[1]s, %[2]s FROM usage_records%[3]s
GROUP BY %[1]s
ORDER BY SUM(input_tokens + output_tokens + cache_read_tokens + cache_write_5m_tokens + cache_write_1h_tokens + total_only_tokens) DESC, %[1]s ASC`,
		col, totalsSelect, where), args...)
	if err != nil {
		return nil, fmt.Errorf("store: breakdown: %w", err)
	}
	defer rows.Close()

	var out []BreakdownRow
	for rows.Next() {
		var key string
		t, err := scanTotals(rows, &key)
		if err != nil {
			return nil, fmt.Errorf("store: breakdown: %w", err)
		}
		out = append(out, BreakdownRow{Key: key, Totals: t})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: breakdown: %w", err)
	}
	return out, nil
}
