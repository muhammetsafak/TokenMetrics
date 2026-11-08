package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

const updateUsageSQL = `
UPDATE usage_records SET
	device_id = ?, ts = ?, service = ?, model = ?, project = ?, session = ?,
	input_tokens = ?, output_tokens = ?, cache_read_tokens = ?,
	cache_write_5m_tokens = ?, cache_write_1h_tokens = ?, thinking_tokens = ?,
	request_count = ?, is_sidechain = ?, reported_cost_usd = ?,
	total_only = ?, total_only_tokens = ?
WHERE source = ? AND external_id = ?
AND (
	device_id IS NOT ? OR ts IS NOT ? OR service IS NOT ? OR model IS NOT ? OR
	project IS NOT ? OR session IS NOT ? OR input_tokens IS NOT ? OR
	output_tokens IS NOT ? OR cache_read_tokens IS NOT ? OR
	cache_write_5m_tokens IS NOT ? OR cache_write_1h_tokens IS NOT ? OR
	thinking_tokens IS NOT ? OR request_count IS NOT ? OR is_sidechain IS NOT ? OR
	reported_cost_usd IS NOT ? OR total_only IS NOT ? OR total_only_tokens IS NOT ?
)`

const insertUsageSQL = `
INSERT INTO usage_records (
	source, external_id, device_id, ts, service, model, project, session,
	input_tokens, output_tokens, cache_read_tokens, cache_write_5m_tokens,
	cache_write_1h_tokens, thinking_tokens, request_count, is_sidechain,
	reported_cost_usd, total_only, total_only_tokens
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT (source, external_id) DO NOTHING`

// recordValues returns the mutable column values in a fixed order shared by
// the SET list and the "changed?" comparison of updateUsageSQL.
func recordValues(r model.UsageRecord) []any {
	var cost any
	if r.ReportedCostUSD != nil {
		cost = *r.ReportedCostUSD
	}
	sidechain := 0
	if r.IsSidechain {
		sidechain = 1
	}
	totalOnly := 0
	if r.TotalOnly {
		totalOnly = 1
	}
	return []any{
		r.DeviceID, r.Timestamp.UnixMilli(), r.Service, r.Model, r.Project, r.Session,
		r.InputTokens, r.OutputTokens, r.CacheReadTokens,
		r.CacheWrite5mTokens, r.CacheWrite1hTokens, r.ThinkingTokens,
		r.RequestCount, sidechain, cost, totalOnly, r.TotalOnlyTokens,
	}
}

// InsertBatch upserts records inside a single transaction.
//
// A record whose (Source, ExternalID) already exists replaces the stored
// values (last write wins), so re-reading the same data never double counts.
// added counts new rows; updated counts existing rows whose values actually
// changed (an identical re-write counts as neither). Records are validated up
// front; if any is invalid nothing is written.
func (s *Store) InsertBatch(ctx context.Context, records []model.UsageRecord) (added, updated int, err error) {
	if len(records) == 0 {
		return 0, 0, nil
	}
	for i, r := range records {
		if err := r.Validate(); err != nil {
			return 0, 0, fmt.Errorf("store: record %d (%s/%s): %w", i, r.Source, r.ExternalID, err)
		}
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, 0, fmt.Errorf("store: begin: %w", err)
	}
	defer tx.Rollback()

	upd, err := tx.PrepareContext(ctx, updateUsageSQL)
	if err != nil {
		return 0, 0, fmt.Errorf("store: prepare update: %w", err)
	}
	defer upd.Close()
	ins, err := tx.PrepareContext(ctx, insertUsageSQL)
	if err != nil {
		return 0, 0, fmt.Errorf("store: prepare insert: %w", err)
	}
	defer ins.Close()

	for _, r := range records {
		vals := recordValues(r)

		// UPDATE args: SET values, key, then the same values for the diff check.
		args := make([]any, 0, 2*len(vals)+2)
		args = append(args, vals...)
		args = append(args, string(r.Source), r.ExternalID)
		args = append(args, vals...)
		res, err := upd.ExecContext(ctx, args...)
		if err != nil {
			return 0, 0, fmt.Errorf("store: update %s/%s: %w", r.Source, r.ExternalID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			updated++
			continue
		}

		// Either a brand-new row, or an existing one with identical values.
		insArgs := make([]any, 0, len(vals)+2)
		insArgs = append(insArgs, string(r.Source), r.ExternalID)
		insArgs = append(insArgs, vals...)
		res, err = ins.ExecContext(ctx, insArgs...)
		if err != nil {
			return 0, 0, fmt.Errorf("store: insert %s/%s: %w", r.Source, r.ExternalID, err)
		}
		if n, _ := res.RowsAffected(); n > 0 {
			added++
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, 0, fmt.Errorf("store: commit: %w", err)
	}
	return added, updated, nil
}

// GetUsageRecord returns the stored record for (source, externalID);
// found is false when there is none.
func (s *Store) GetUsageRecord(ctx context.Context, source model.Source, externalID string) (rec model.UsageRecord, found bool, err error) {
	rec, err = scanUsageRecord(s.db.QueryRowContext(ctx,
		"SELECT "+usageColumns+" FROM usage_records WHERE source = ? AND external_id = ?",
		string(source), externalID))
	if errors.Is(err, sql.ErrNoRows) {
		return model.UsageRecord{}, false, nil
	}
	if err != nil {
		return model.UsageRecord{}, false, fmt.Errorf("store: get usage record: %w", err)
	}
	return rec, true, nil
}
