package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Limit window kinds.
const (
	LimitKind5h     = "5h"
	LimitKindWeekly = "weekly"
)

// Limit metrics: what a limit window counts.
const (
	MetricTotalTokens  = "total_tokens"
	MetricOutputTokens = "output_tokens"
	MetricCostUSD      = "cost_usd"
)

// LimitWindow is a user-entered, estimated subscription limit.
//
// Limit is stored in the integer column token_limit. For the token metrics it
// is a token count; for MetricCostUSD it is the limit in US cents.
type LimitWindow struct {
	ID        int64
	Service   string // "" = all services
	Kind      string // LimitKind5h or LimitKindWeekly
	Limit     int64
	Metric    string
	UpdatedAt time.Time
}

func (l LimitWindow) validate() error {
	switch l.Kind {
	case LimitKind5h, LimitKindWeekly:
	default:
		return fmt.Errorf("unknown kind %q", l.Kind)
	}
	switch l.Metric {
	case MetricTotalTokens, MetricOutputTokens, MetricCostUSD:
	default:
		return fmt.Errorf("unknown metric %q", l.Metric)
	}
	if l.Limit <= 0 {
		return errors.New("limit must be positive")
	}
	return nil
}

// ListLimitWindows returns all limit windows ordered by service, kind.
func (s *Store) ListLimitWindows(ctx context.Context) ([]LimitWindow, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, service, kind, token_limit, metric, updated_at
FROM limit_windows ORDER BY service, kind, id`)
	if err != nil {
		return nil, fmt.Errorf("store: list limit windows: %w", err)
	}
	defer rows.Close()

	var out []LimitWindow
	for rows.Next() {
		var l LimitWindow
		var updated int64
		if err := rows.Scan(&l.ID, &l.Service, &l.Kind, &l.Limit, &l.Metric, &updated); err != nil {
			return nil, fmt.Errorf("store: scan limit window: %w", err)
		}
		l.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list limit windows: %w", err)
	}
	return out, nil
}

// SetLimitWindow creates the limit for (Service, Kind) or replaces the
// existing one, so there is at most one limit per service and kind. It
// returns the row id. UpdatedAt is set to the current time.
func (s *Store) SetLimitWindow(ctx context.Context, l LimitWindow) (int64, error) {
	if err := l.validate(); err != nil {
		return 0, fmt.Errorf("store: set limit window: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, fmt.Errorf("store: set limit window: %w", err)
	}
	defer tx.Rollback()

	now := time.Now().UnixMilli()
	var id int64
	err = tx.QueryRowContext(ctx,
		`SELECT id FROM limit_windows WHERE service = ? AND kind = ? ORDER BY id LIMIT 1`,
		l.Service, l.Kind).Scan(&id)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		res, err := tx.ExecContext(ctx, `
INSERT INTO limit_windows (service, kind, token_limit, metric, updated_at)
VALUES (?, ?, ?, ?, ?)`, l.Service, l.Kind, l.Limit, l.Metric, now)
		if err != nil {
			return 0, fmt.Errorf("store: insert limit window: %w", err)
		}
		if id, err = res.LastInsertId(); err != nil {
			return 0, fmt.Errorf("store: insert limit window: %w", err)
		}
	case err != nil:
		return 0, fmt.Errorf("store: set limit window: %w", err)
	default:
		if _, err := tx.ExecContext(ctx, `
UPDATE limit_windows SET token_limit = ?, metric = ?, updated_at = ? WHERE id = ?`,
			l.Limit, l.Metric, now, id); err != nil {
			return 0, fmt.Errorf("store: update limit window: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("store: set limit window: %w", err)
	}
	return id, nil
}

// DeleteLimitWindow removes the limit with the given id; a missing one is not an error.
func (s *Store) DeleteLimitWindow(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM limit_windows WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete limit window %d: %w", id, err)
	}
	return nil
}
