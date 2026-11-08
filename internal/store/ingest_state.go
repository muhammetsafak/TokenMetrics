package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/muhammetsafak/TokenMetrics/internal/model"
)

// IngestState is the incremental read position of one source file (see docs/design.md).
type IngestState struct {
	Source    model.Source
	Path      string
	Offset    int64     // bytes already consumed
	Size      int64     // file size when last read
	ModTime   time.Time // file mtime when last read (nanosecond precision)
	UpdatedAt time.Time // set by SetIngestState
}

// GetIngestState returns the saved state for (source, path); found is false
// when the file has never been read.
func (s *Store) GetIngestState(ctx context.Context, source model.Source, path string) (st IngestState, found bool, err error) {
	var mtime, updated int64
	err = s.db.QueryRowContext(ctx,
		`SELECT "offset", size, mtime, updated_at FROM ingest_state WHERE source = ? AND path = ?`,
		string(source), path).Scan(&st.Offset, &st.Size, &mtime, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		return IngestState{}, false, nil
	}
	if err != nil {
		return IngestState{}, false, fmt.Errorf("store: get ingest state: %w", err)
	}
	st.Source, st.Path = source, path
	st.ModTime = modTimeFromDB(mtime)
	st.UpdatedAt = time.UnixMilli(updated).UTC()
	return st, true, nil
}

// SetIngestState inserts or replaces the state for (st.Source, st.Path).
// UpdatedAt is stamped with the current time.
func (s *Store) SetIngestState(ctx context.Context, st IngestState) error {
	if !st.Source.Valid() {
		return fmt.Errorf("store: ingest state: invalid source %q", st.Source)
	}
	if st.Path == "" {
		return errors.New("store: ingest state: empty path")
	}
	if st.Offset < 0 || st.Size < 0 {
		return errors.New("store: ingest state: negative offset or size")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO ingest_state (source, path, "offset", size, mtime, updated_at)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT (source, path) DO UPDATE SET
	"offset" = excluded."offset", size = excluded.size,
	mtime = excluded.mtime, updated_at = excluded.updated_at`,
		string(st.Source), st.Path, st.Offset, st.Size, modTimeToDB(st.ModTime), time.Now().UnixMilli())
	if err != nil {
		return fmt.Errorf("store: set ingest state: %w", err)
	}
	return nil
}

// ListIngestStates returns all saved states for a source, ordered by path.
func (s *Store) ListIngestStates(ctx context.Context, source model.Source) ([]IngestState, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT path, "offset", size, mtime, updated_at FROM ingest_state WHERE source = ? ORDER BY path`,
		string(source))
	if err != nil {
		return nil, fmt.Errorf("store: list ingest states: %w", err)
	}
	defer rows.Close()

	var out []IngestState
	for rows.Next() {
		st := IngestState{Source: source}
		var mtime, updated int64
		if err := rows.Scan(&st.Path, &st.Offset, &st.Size, &mtime, &updated); err != nil {
			return nil, fmt.Errorf("store: list ingest states: %w", err)
		}
		st.ModTime = modTimeFromDB(mtime)
		st.UpdatedAt = time.UnixMilli(updated).UTC()
		out = append(out, st)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("store: list ingest states: %w", err)
	}
	return out, nil
}

// modTimeToDB stores the zero time as 0: time.Time{}.UnixNano() is outside
// the int64 range and would otherwise be stored as an overflowed value.
func modTimeToDB(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.UnixNano()
}

// modTimeFromDB is the inverse of modTimeToDB; 0 means "unknown" (zero time).
func modTimeFromDB(ns int64) time.Time {
	if ns == 0 {
		return time.Time{}
	}
	return time.Unix(0, ns).UTC()
}
