package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
)

// GetSetting loads the JSON value stored under key into dst (a pointer, as for
// json.Unmarshal). found is false when the key is not set; dst is untouched.
func (s *Store) GetSetting(ctx context.Context, key string, dst any) (found bool, err error) {
	var raw string
	err = s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&raw)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("store: get setting %q: %w", key, err)
	}
	if err := json.Unmarshal([]byte(raw), dst); err != nil {
		return false, fmt.Errorf("store: decode setting %q: %w", key, err)
	}
	return true, nil
}

// SetSetting stores value, JSON-encoded, under key (insert or replace).
func (s *Store) SetSetting(ctx context.Context, key string, value any) error {
	if key == "" {
		return errors.New("store: empty setting key")
	}
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("store: encode setting %q: %w", key, err)
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO settings (key, value) VALUES (?, ?)
ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, string(b))
	if err != nil {
		return fmt.Errorf("store: set setting %q: %w", key, err)
	}
	return nil
}

// DeleteSetting removes key; it is not an error if the key does not exist.
func (s *Store) DeleteSetting(ctx context.Context, key string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM settings WHERE key = ?`, key); err != nil {
		return fmt.Errorf("store: delete setting %q: %w", key, err)
	}
	return nil
}
