package store

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// EachUsageRecord streams every record matching f to fn, ordered by
// timestamp, then source, then external id. Iteration stops at the first error
// fn returns, which is passed back unchanged. fn must not write to the store:
// the read cursor is still open while it runs.
func (s *Store) EachUsageRecord(ctx context.Context, f Filter, fn func(model.UsageRecord) error) error {
	where, args := f.where()
	rows, err := s.db.QueryContext(ctx, "SELECT "+usageColumns+" FROM usage_records"+where+`
ORDER BY ts, source, external_id`, args...)
	if err != nil {
		return fmt.Errorf("store: list usage records: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		r, err := scanUsageRecord(rows)
		if err != nil {
			return fmt.Errorf("store: list usage records: %w", err)
		}
		if err := fn(r); err != nil {
			return err
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("store: list usage records: %w", err)
	}
	return nil
}

// usageColumns is the column list scanUsageRecord reads, in scan order.
// GetUsageRecord (usage.go) reads through the same helpers.
const usageColumns = `source, external_id, device_id, ts, service, model, project, session,
	input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens,
	cache_write_1h_tokens, thinking_tokens, request_count, is_sidechain,
	reported_cost_usd, total_only, total_only_tokens`

func scanUsageRecord(sc scanner) (model.UsageRecord, error) {
	var (
		r         model.UsageRecord
		src       string
		ts        int64
		sidechain int64
		totalOnly int64
		cost      sql.NullFloat64
	)
	if err := sc.Scan(
		&src, &r.ExternalID, &r.DeviceID, &ts, &r.Service, &r.Model, &r.Project, &r.Session,
		&r.InputTokens, &r.OutputTokens, &r.CacheReadTokens, &r.CacheWrite5mTokens,
		&r.CacheWrite1hTokens, &r.ThinkingTokens, &r.RequestCount, &sidechain, &cost,
		&totalOnly, &r.TotalOnlyTokens); err != nil {
		return model.UsageRecord{}, err
	}
	r.Source = model.Source(src)
	r.Timestamp = time.UnixMilli(ts).UTC()
	r.IsSidechain = sidechain != 0
	r.TotalOnly = totalOnly != 0
	if cost.Valid {
		v := cost.Float64
		r.ReportedCostUSD = &v
	}
	return r, nil
}

// UpdateManualRecord replaces the values of the existing manual record with
// r.ExternalID. Records from any other source are never touched. found is
// false when no manual record has that id; r.Source is ignored.
func (s *Store) UpdateManualRecord(ctx context.Context, r model.UsageRecord) (found bool, err error) {
	r.Source = model.SourceManual
	if err := r.Validate(); err != nil {
		return false, fmt.Errorf("store: update manual record %s: %w", r.ExternalID, err)
	}
	vals := recordValues(r)
	args := append(vals, string(model.SourceManual), r.ExternalID)
	res, err := s.db.ExecContext(ctx, `
UPDATE usage_records SET
	device_id = ?, ts = ?, service = ?, model = ?, project = ?, session = ?,
	input_tokens = ?, output_tokens = ?, cache_read_tokens = ?,
	cache_write_5m_tokens = ?, cache_write_1h_tokens = ?, thinking_tokens = ?,
	request_count = ?, is_sidechain = ?, reported_cost_usd = ?,
	total_only = ?, total_only_tokens = ?
WHERE source = ? AND external_id = ?`, args...)
	if err != nil {
		return false, fmt.Errorf("store: update manual record %s: %w", r.ExternalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: update manual record %s: %w", r.ExternalID, err)
	}
	return n > 0, nil
}

// DeleteManualRecord removes the manual record with the given id. Records from
// any other source are never touched. found is false when there was none.
func (s *Store) DeleteManualRecord(ctx context.Context, externalID string) (found bool, err error) {
	res, err := s.db.ExecContext(ctx,
		`DELETE FROM usage_records WHERE source = ? AND external_id = ?`,
		string(model.SourceManual), externalID)
	if err != nil {
		return false, fmt.Errorf("store: delete manual record %s: %w", externalID, err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: delete manual record %s: %w", externalID, err)
	}
	return n > 0, nil
}
