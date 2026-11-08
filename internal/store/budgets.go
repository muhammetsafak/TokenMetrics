package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"math"
	"time"
)

// Budget scopes and periods.
const (
	BudgetScopeTotal   = "total"
	BudgetScopeService = "service"
	BudgetScopeModel   = "model"

	BudgetPeriodDaily   = "daily"
	BudgetPeriodMonthly = "monthly"
)

// Budget is a spending/usage rule. At least one threshold is set; when both
// are, reaching either one is a breach.
type Budget struct {
	ID              int64
	Scope           string // BudgetScope*
	ScopeValue      string // service or model name; "" for total
	Period          string // BudgetPeriod*
	ThresholdTokens *int64
	ThresholdUSD    *float64
	Enabled         bool
}

func (b Budget) validate() error {
	switch b.Scope {
	case BudgetScopeTotal:
	case BudgetScopeService, BudgetScopeModel:
		if b.ScopeValue == "" {
			return fmt.Errorf("scope %q needs a scope value", b.Scope)
		}
	default:
		return fmt.Errorf("unknown scope %q", b.Scope)
	}
	switch b.Period {
	case BudgetPeriodDaily, BudgetPeriodMonthly:
	default:
		return fmt.Errorf("unknown period %q", b.Period)
	}
	if b.ThresholdTokens == nil && b.ThresholdUSD == nil {
		return errors.New("no threshold set")
	}
	if b.ThresholdTokens != nil && *b.ThresholdTokens <= 0 {
		return errors.New("token threshold must be positive")
	}
	if b.ThresholdUSD != nil && (math.IsNaN(*b.ThresholdUSD) || math.IsInf(*b.ThresholdUSD, 0) || *b.ThresholdUSD <= 0) {
		return errors.New("usd threshold must be positive")
	}
	return nil
}

func (b Budget) args() []any {
	var tok, usd any
	if b.ThresholdTokens != nil {
		tok = *b.ThresholdTokens
	}
	if b.ThresholdUSD != nil {
		usd = *b.ThresholdUSD
	}
	scopeValue := b.ScopeValue
	if b.Scope == BudgetScopeTotal {
		scopeValue = ""
	}
	enabled := 0
	if b.Enabled {
		enabled = 1
	}
	return []any{b.Scope, scopeValue, b.Period, tok, usd, enabled}
}

// CreateBudget inserts b (ID is ignored) and returns the new row id.
func (s *Store) CreateBudget(ctx context.Context, b Budget) (int64, error) {
	if err := b.validate(); err != nil {
		return 0, fmt.Errorf("store: create budget: %w", err)
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO budgets (scope, scope_value, period, threshold_tokens, threshold_usd, enabled)
VALUES (?, ?, ?, ?, ?, ?)`, b.args()...)
	if err != nil {
		return 0, fmt.Errorf("store: create budget: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("store: create budget: %w", err)
	}
	return id, nil
}

// UpdateBudget replaces the budget with b.ID. It reports an error wrapping
// sql.ErrNoRows when the budget does not exist.
//
// When the rule itself changes (scope, scope value, period or a threshold) the
// notifications already recorded for the budget are dropped in the same
// transaction: they were sent for the old rule, and the new one must be able
// to alert again within the current period. Changing only Enabled keeps them.
func (s *Store) UpdateBudget(ctx context.Context, b Budget) error {
	if err := b.validate(); err != nil {
		return fmt.Errorf("store: update budget: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: update budget %d: %w", b.ID, err)
	}
	defer tx.Rollback()

	var scope, scopeValue, period string
	var tok sql.NullInt64
	var usd sql.NullFloat64
	err = tx.QueryRowContext(ctx,
		`SELECT scope, scope_value, period, threshold_tokens, threshold_usd FROM budgets WHERE id = ?`, b.ID).
		Scan(&scope, &scopeValue, &period, &tok, &usd)
	if errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("store: update budget %d: %w", b.ID, sql.ErrNoRows)
	}
	if err != nil {
		return fmt.Errorf("store: update budget %d: %w", b.ID, err)
	}

	args := b.args() // scope, scope_value, period, tokens, usd, enabled
	ruleChanged := scope != args[0] || scopeValue != args[1] || period != args[2] ||
		!sameNullable(tok.Valid, tok.Int64, args[3]) ||
		!sameNullable(usd.Valid, usd.Float64, args[4])

	if _, err := tx.ExecContext(ctx, `
UPDATE budgets SET scope = ?, scope_value = ?, period = ?,
	threshold_tokens = ?, threshold_usd = ?, enabled = ?
WHERE id = ?`, append(args, b.ID)...); err != nil {
		return fmt.Errorf("store: update budget %d: %w", b.ID, err)
	}
	if ruleChanged {
		if _, err := tx.ExecContext(ctx, `DELETE FROM notifications_sent WHERE budget_id = ?`, b.ID); err != nil {
			return fmt.Errorf("store: update budget %d: reset notifications: %w", b.ID, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: update budget %d: %w", b.ID, err)
	}
	return nil
}

// sameNullable compares a nullable column value (valid, v) with a new value
// that is nil or a plain T.
func sameNullable[T int64 | float64](valid bool, v T, next any) bool {
	if next == nil {
		return !valid
	}
	n, ok := next.(T)
	return valid && ok && n == v
}

// DeleteBudget removes the budget and (by cascade) its notification records;
// a missing budget is not an error.
func (s *Store) DeleteBudget(ctx context.Context, id int64) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM budgets WHERE id = ?`, id); err != nil {
		return fmt.Errorf("store: delete budget %d: %w", id, err)
	}
	return nil
}

// ListBudgets returns budgets ordered by id; with onlyEnabled the disabled
// ones are left out.
func (s *Store) ListBudgets(ctx context.Context, onlyEnabled bool) ([]Budget, error) {
	q := `SELECT id, scope, scope_value, period, threshold_tokens, threshold_usd, enabled FROM budgets`
	if onlyEnabled {
		q += ` WHERE enabled = 1`
	}
	rows, err := s.db.QueryContext(ctx, q+` ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("store: list budgets: %w", err)
	}
	defer rows.Close()

	var out []Budget
	for rows.Next() {
		var b Budget
		var tok sql.NullInt64
		var usd sql.NullFloat64
		var enabled int
		if err := rows.Scan(&b.ID, &b.Scope, &b.ScopeValue, &b.Period, &tok, &usd, &enabled); err != nil {
			return nil, fmt.Errorf("store: scan budget: %w", err)
		}
		if tok.Valid {
			v := tok.Int64
			b.ThresholdTokens = &v
		}
		b.ThresholdUSD = floatPtr(usd)
		b.Enabled = enabled != 0
		out = append(out, b)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list budgets: %w", err)
	}
	return out, nil
}

// NotificationSent reports whether a notification for (budgetID, periodKey)
// was already recorded.
func (s *Store) NotificationSent(ctx context.Context, budgetID int64, periodKey string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM notifications_sent WHERE budget_id = ? AND period_key = ?`,
		budgetID, periodKey).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: notification sent: %w", err)
	}
	return true, nil
}

// MarkNotificationSent records a notification for (budgetID, periodKey). It
// returns false when one was already recorded (nothing changes then).
func (s *Store) MarkNotificationSent(ctx context.Context, budgetID int64, periodKey string, at time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `
INSERT INTO notifications_sent (budget_id, period_key, sent_at) VALUES (?, ?, ?)
ON CONFLICT (budget_id, period_key) DO NOTHING`, budgetID, periodKey, at.UnixMilli())
	if err != nil {
		return false, fmt.Errorf("store: mark notification sent: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("store: mark notification sent: %w", err)
	}
	return n > 0, nil
}
