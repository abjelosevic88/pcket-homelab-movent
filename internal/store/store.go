// Package store wraps the SQLite database, migrations and all queries.
package store

import (
	"context"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

// ErrNotFound is returned when a row does not exist.
var ErrNotFound = errors.New("not found")

// ErrConflict is returned when a unique constraint is violated.
var ErrConflict = errors.New("already exists")

// Store is the database handle.
type Store struct {
	DB  *sql.DB
	log *slog.Logger
}

// Open opens (and creates if needed) the SQLite database and runs migrations.
func Open(path string, log *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := fmt.Sprintf("file:%s?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(ON)&_pragma=synchronous(NORMAL)", path)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite works best with a single writer.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(`PRAGMA cache_size = -32000`); err != nil {
		return nil, err
	}
	db.SetConnMaxLifetime(0)
	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("open database: %w", err)
	}
	s := &Store{DB: db, log: log}
	if err := s.migrate(); err != nil {
		return nil, err
	}
	return s, nil
}

// Close closes the database.
func (s *Store) Close() error { return s.DB.Close() }

func (s *Store) migrate() error {
	if _, err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (name TEXT PRIMARY KEY, applied_at TEXT NOT NULL)`); err != nil {
		return err
	}
	entries, err := fs.ReadDir(migrationFS, "migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sql") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		var exists int
		if err := s.DB.QueryRow(`SELECT COUNT(1) FROM schema_migrations WHERE name = ?`, name).Scan(&exists); err != nil {
			return err
		}
		if exists > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := s.DB.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(string(body)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %s: %w", name, err)
		}
		if _, err := tx.Exec(`INSERT INTO schema_migrations (name, applied_at) VALUES (?, ?)`, name, Now()); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		s.log.Info("applied migration", "name", name)
	}
	return nil
}

// Backup writes a consistent copy of the database to dst using VACUUM INTO.
func (s *Store) Backup(ctx context.Context, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	_, err := s.DB.ExecContext(ctx, `VACUUM INTO ?`, dst)
	return err
}

// Counts returns row counts per table for metrics / the About page.
func (s *Store) Counts(ctx context.Context) map[string]int {
	out := map[string]int{}
	for _, t := range []string{"users", "devices", "points", "places", "visits", "tracks", "days", "imports", "shares", "api_tokens"} {
		var n int
		if err := s.DB.QueryRowContext(ctx, `SELECT COUNT(1) FROM `+t).Scan(&n); err == nil {
			out[t] = n
		}
	}
	return out
}

// Now returns the current UTC time formatted for storage.
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }

// Today returns today's date (local) as YYYY-MM-DD.
func Today() string { return time.Now().Format("2006-01-02") }

// ParseDate parses YYYY-MM-DD.
func ParseDate(s string) (time.Time, error) { return time.Parse("2006-01-02", s) }

func notFound(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	return err
}

func conflict(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return ErrConflict
	}
	return err
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
