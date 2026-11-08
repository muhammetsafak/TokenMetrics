// Package store is the SQLite persistence layer (modernc.org/sqlite, no CGO).
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"strings"

	_ "modernc.org/sqlite" // registers the "sqlite" database/sql driver
)

// Store wraps the SQLite database. It is safe for concurrent use.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens (creating if needed) the database at path, enables WAL and
// foreign keys, and applies all pending migrations. The parent directory
// must exist (see the paths package).
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("store: empty database path")
	}
	if strings.ContainsRune(path, '?') {
		return nil, fmt.Errorf("store: database path must not contain '?': %q", path)
	}

	q := url.Values{}
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "synchronous(NORMAL)")
	// Write transactions take the write lock up front, so concurrent writers
	// wait on busy_timeout instead of failing on a lock upgrade.
	q.Set("_txlock", "immediate")

	db, err := sql.Open("sqlite", path+"?"+q.Encode())
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := migrateUp(ctx, db); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: %w", err)
	}
	return &Store{db: db, path: path}, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.db.Close() }

// Path returns the database file path.
func (s *Store) Path() string { return s.path }

// SchemaVersion returns the highest applied migration version.
func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	return schemaVersion(ctx, s.db)
}
