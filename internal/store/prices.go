package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// PriceOverride is a user-edited price for one model, in USD per million
// tokens. A nil component means "no override for this component: use the
// built-in price".
type PriceOverride struct {
	Model               string
	InputPerMTok        *float64
	OutputPerMTok       *float64
	CacheReadPerMTok    *float64
	CacheWrite5mPerMTok *float64
	CacheWrite1hPerMTok *float64
	UpdatedAt           time.Time
}

func (o PriceOverride) components() []*float64 {
	return []*float64{
		o.InputPerMTok, o.OutputPerMTok, o.CacheReadPerMTok,
		o.CacheWrite5mPerMTok, o.CacheWrite1hPerMTok,
	}
}

func (o PriceOverride) validate() error {
	if strings.TrimSpace(o.Model) == "" {
		return errors.New("model is empty")
	}
	hasComponent := false
	for _, c := range o.components() {
		if c == nil {
			continue
		}
		hasComponent = true
		if math.IsNaN(*c) || math.IsInf(*c, 0) || *c < 0 {
			return fmt.Errorf("price %v is not a non-negative number", *c)
		}
	}
	if !hasComponent {
		return errors.New("no price component set")
	}
	return nil
}

// overrideKey is the form in which an override's model name is stored: trimmed
// and lower-cased, so "Claude-X" and "claude-x" are the same override.
func overrideKey(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func floatPtr(n sql.NullFloat64) *float64 {
	if !n.Valid {
		return nil
	}
	v := n.Float64
	return &v
}

// ListPriceOverrides returns all overrides ordered by model.
func (s *Store) ListPriceOverrides(ctx context.Context) ([]PriceOverride, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT model, input_per_mtok, output_per_mtok, cache_read_per_mtok,
       cache_write_5m_per_mtok, cache_write_1h_per_mtok, updated_at
FROM price_overrides ORDER BY model`)
	if err != nil {
		return nil, fmt.Errorf("store: list price overrides: %w", err)
	}
	defer rows.Close()

	var out []PriceOverride
	for rows.Next() {
		var o PriceOverride
		var in, outp, cr, w5, w1 sql.NullFloat64
		var updated int64
		if err := rows.Scan(&o.Model, &in, &outp, &cr, &w5, &w1, &updated); err != nil {
			return nil, fmt.Errorf("store: scan price override: %w", err)
		}
		o.InputPerMTok, o.OutputPerMTok, o.CacheReadPerMTok = floatPtr(in), floatPtr(outp), floatPtr(cr)
		o.CacheWrite5mPerMTok, o.CacheWrite1hPerMTok = floatPtr(w5), floatPtr(w1)
		o.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, o)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list price overrides: %w", err)
	}
	return out, nil
}

// SetPriceOverride inserts or replaces the override for o.Model (matched
// case-insensitively; stored trimmed and lower-cased). UpdatedAt is set to the
// current time.
func (s *Store) SetPriceOverride(ctx context.Context, o PriceOverride) error {
	if err := o.validate(); err != nil {
		return fmt.Errorf("store: set price override: %w", err)
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO price_overrides (
	model, input_per_mtok, output_per_mtok, cache_read_per_mtok,
	cache_write_5m_per_mtok, cache_write_1h_per_mtok, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (model) DO UPDATE SET
	input_per_mtok = excluded.input_per_mtok,
	output_per_mtok = excluded.output_per_mtok,
	cache_read_per_mtok = excluded.cache_read_per_mtok,
	cache_write_5m_per_mtok = excluded.cache_write_5m_per_mtok,
	cache_write_1h_per_mtok = excluded.cache_write_1h_per_mtok,
	updated_at = excluded.updated_at`,
		overrideKey(o.Model),
		nullFloat(o.InputPerMTok), nullFloat(o.OutputPerMTok), nullFloat(o.CacheReadPerMTok),
		nullFloat(o.CacheWrite5mPerMTok), nullFloat(o.CacheWrite1hPerMTok),
		time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: set price override %q: %w", o.Model, err)
	}
	return nil
}

// DeletePriceOverride removes the override for model (matched like
// SetPriceOverride does); a missing one is not an error.
func (s *Store) DeletePriceOverride(ctx context.Context, model string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM price_overrides WHERE model = ?`, overrideKey(model)); err != nil {
		return fmt.Errorf("store: delete price override %q: %w", model, err)
	}
	return nil
}

// TokenCounts is a set of token sums.
type TokenCounts struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWrite5mTokens int64
	CacheWrite1hTokens int64
}

// CostInput is the data needed to estimate the cost of the records of one
// model. Token counts cover only records that did NOT report their own cost
// (those are priced from a table); records that did are summarized by
// ReportedCostUSD and ReportedRecords.
type CostInput struct {
	Model string
	TokenCounts

	// Long is the part of the token counts above that belongs to records
	// whose prompt (input + cache read + cache writes, per request) is over
	// the threshold given to CostInputs. Some models charge more for those.
	Long TokenCounts

	// TierUnknownRecords counts the unreported records whose request count is
	// unknown (0) and whose prompt is over the threshold: they cannot be
	// assigned to a tier, so they are NOT in Long.
	TierUnknownRecords int64

	UnreportedRecords int64
	ReportedRecords   int64
	ReportedCostUSD   float64

	// TotalOnlyRecords and TotalOnlyTokens describe the records that carry
	// only a combined token count. They cannot be priced, so they are not in
	// UnreportedRecords or the counts above; they are only counted here. A
	// total-only record that did report a cost is in ReportedRecords /
	// ReportedCostUSD as usual, and not here.
	TotalOnlyRecords int64
	TotalOnlyTokens  int64
}

// CostInputs returns one CostInput per distinct model among the records
// matching f, ordered by model.
//
// longPromptThreshold fills CostInput.Long: a record counts as long when its
// prompt tokens exceed longPromptThreshold per request (RequestCount, so an
// aggregated record is judged by its average request). A record with
// RequestCount 0 (unknown, e.g. a daily API usage bucket) is never long; it is
// counted in TierUnknownRecords instead when its prompt is over the threshold.
// Zero or negative disables the split.
func (s *Store) CostInputs(ctx context.Context, f Filter, longPromptThreshold int64) ([]CostInput, error) {
	where, args := f.where()

	const unreported = `reported_cost_usd IS NULL AND total_only = 0`
	const prompt = `(input_tokens + cache_read_tokens + cache_write_5m_tokens + cache_write_1h_tokens)`
	long, tierUnknown := `0`, `0` // never true
	var longArgs []any
	if longPromptThreshold > 0 {
		long = unreported + ` AND request_count > 0 AND ` + prompt + ` > ? * request_count`
		tierUnknown = unreported + ` AND request_count = 0 AND ` + prompt + ` > ?`
	}
	sum := func(cond, col string) string {
		return `COALESCE(SUM(CASE WHEN ` + cond + ` THEN ` + col + ` ELSE 0 END), 0)`
	}

	cols := []string{"input_tokens", "output_tokens", "cache_read_tokens", "cache_write_5m_tokens", "cache_write_1h_tokens"}
	q := `SELECT model`
	for _, c := range cols {
		q += `, ` + sum(unreported, c)
	}
	for _, c := range cols {
		q += `, ` + sum(long, c)
		if longPromptThreshold > 0 {
			longArgs = append(longArgs, longPromptThreshold)
		}
	}
	q += `, ` + sum(tierUnknown, "1")
	if longPromptThreshold > 0 {
		longArgs = append(longArgs, longPromptThreshold)
	}
	q += `, ` + sum(unreported, "1") + `, COUNT(reported_cost_usd), COALESCE(SUM(reported_cost_usd), 0)`
	const uncostedTotalOnly = `reported_cost_usd IS NULL AND total_only = 1`
	q += `, ` + sum(uncostedTotalOnly, "1") + `, ` + sum(uncostedTotalOnly, "total_only_tokens")
	q += ` FROM usage_records` + where + ` GROUP BY model ORDER BY model`

	rows, err := s.db.QueryContext(ctx, q, append(longArgs, args...)...)
	if err != nil {
		return nil, fmt.Errorf("store: cost inputs: %w", err)
	}
	defer rows.Close()

	var out []CostInput
	for rows.Next() {
		var c CostInput
		if err := rows.Scan(&c.Model,
			&c.InputTokens, &c.OutputTokens, &c.CacheReadTokens, &c.CacheWrite5mTokens, &c.CacheWrite1hTokens,
			&c.Long.InputTokens, &c.Long.OutputTokens, &c.Long.CacheReadTokens, &c.Long.CacheWrite5mTokens, &c.Long.CacheWrite1hTokens,
			&c.TierUnknownRecords, &c.UnreportedRecords, &c.ReportedRecords, &c.ReportedCostUSD,
			&c.TotalOnlyRecords, &c.TotalOnlyTokens); err != nil {
			return nil, fmt.Errorf("store: scan cost input: %w", err)
		}
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: cost inputs: %w", err)
	}
	return out, nil
}
