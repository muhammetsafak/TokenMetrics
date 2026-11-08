package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"time"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// Migration files: NNNN_name.sql (up) with an optional NNNN_name.down.sql.
var (
	upFileRE   = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.sql$`)
	downFileRE = regexp.MustCompile(`^(\d{4})_[a-z0-9_]+\.down\.sql$`)
)

type migration struct {
	version int
	name    string
	up      string
	down    string // empty when the migration has no down script
}

// loadMigrations reads the embedded migration files, sorted by version.
func loadMigrations() ([]migration, error) {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	byVersion := map[int]*migration{}
	get := func(v int) *migration {
		m, ok := byVersion[v]
		if !ok {
			m = &migration{version: v}
			byVersion[v] = m
		}
		return m
	}
	for _, e := range entries {
		name := e.Name()
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		if m := downFileRE.FindStringSubmatch(name); m != nil {
			v, _ := strconv.Atoi(m[1])
			get(v).down = string(body)
		} else if m := upFileRE.FindStringSubmatch(name); m != nil {
			v, _ := strconv.Atoi(m[1])
			mg := get(v)
			if mg.up != "" {
				return nil, fmt.Errorf("duplicate migration version %04d", v)
			}
			mg.name = name
			mg.up = string(body)
		} else {
			return nil, fmt.Errorf("unrecognized migration file %q", name)
		}
	}
	out := make([]migration, 0, len(byVersion))
	for _, m := range byVersion {
		if m.up == "" {
			return nil, fmt.Errorf("migration %04d has a down script but no up script", m.version)
		}
		out = append(out, *m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].version < out[j].version })
	for i, m := range out {
		if m.version != i+1 {
			return nil, fmt.Errorf("migration versions are not contiguous: expected %04d, found %04d", i+1, m.version)
		}
	}
	return out, nil
}

const createSchemaVersion = `CREATE TABLE IF NOT EXISTS schema_version (
	version    INTEGER PRIMARY KEY,
	applied_at INTEGER NOT NULL
)`

// migrateUp applies every pending migration. Running it again is a no-op.
func migrateUp(ctx context.Context, db *sql.DB) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, createSchemaVersion); err != nil {
		return fmt.Errorf("create schema_version: %w", err)
	}
	for _, m := range migs {
		if err := applyUp(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyUp(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil) // immediate lock, see Open
	if err != nil {
		return fmt.Errorf("begin migration %04d: %w", m.version, err)
	}
	defer tx.Rollback()

	var applied int
	if err := tx.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM schema_version WHERE version = ?`, m.version).Scan(&applied); err != nil {
		return fmt.Errorf("check migration %04d: %w", m.version, err)
	}
	if applied > 0 {
		return nil // already applied (possibly by another process)
	}
	if _, err := tx.ExecContext(ctx, m.up); err != nil {
		return fmt.Errorf("apply migration %s: %w", m.name, err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO schema_version (version, applied_at) VALUES (?, ?)`,
		m.version, time.Now().UnixMilli()); err != nil {
		return fmt.Errorf("record migration %04d: %w", m.version, err)
	}
	return tx.Commit()
}

// migrateDown rolls back migrations until the schema is at target version.
// It is not used at runtime; it exists so every migration stays reversible.
func migrateDown(ctx context.Context, db *sql.DB, target int) error {
	migs, err := loadMigrations()
	if err != nil {
		return err
	}
	for i := len(migs) - 1; i >= 0; i-- {
		m := migs[i]
		if m.version <= target {
			break
		}
		if m.down == "" {
			return fmt.Errorf("migration %04d has no down script", m.version)
		}
		if err := applyDown(ctx, db, m); err != nil {
			return err
		}
	}
	return nil
}

func applyDown(ctx context.Context, db *sql.DB, m migration) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollback %04d: %w", m.version, err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(ctx, m.down); err != nil {
		return fmt.Errorf("roll back migration %04d: %w", m.version, err)
	}
	if _, err := tx.ExecContext(ctx,
		`DELETE FROM schema_version WHERE version = ?`, m.version); err != nil {
		return fmt.Errorf("unrecord migration %04d: %w", m.version, err)
	}
	return tx.Commit()
}

// schemaVersion returns the highest applied migration version (0 if none).
func schemaVersion(ctx context.Context, db *sql.DB) (int, error) {
	var v sql.NullInt64
	if err := db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_version`).Scan(&v); err != nil {
		return 0, fmt.Errorf("read schema_version: %w", err)
	}
	return int(v.Int64), nil
}
