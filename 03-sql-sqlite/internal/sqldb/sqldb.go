// Package sqldb holds the small helpers shared by every lesson in the SQL track:
// opening a SQLite database with sensible settings and loading the sample schema + data.
//
// It is deliberately tiny. In a real service this code lives in an `internal/db` or
// `internal/store` package and also owns pool settings, health checks and error mapping.
package sqldb

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver; pure Go, no cgo needed

	sqlfiles "github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/sql"
)

// DSN builds a modernc.org/sqlite connection string for a file path.
//
// Pragmas are applied to EVERY new connection the pool opens, which matters because:
//   - foreign_keys is OFF by default in SQLite and is a per-connection setting. Without the
//     pragma in the DSN, only the first connection would enforce FKs and bugs would appear randomly.
//   - journal_mode(WAL) lets readers and one writer work at the same time.
//   - busy_timeout(5000) makes a blocked writer wait up to 5s instead of failing instantly
//     with "database is locked".
//
// IN-MEMORY GOTCHA: with ":memory:" every connection in the pool gets its OWN private empty
// database. Tables created on one connection are invisible on the next one, giving confusing
// "no such table" errors. Fix: db.SetMaxOpenConns(1), or (what we do) use a temp file.
func DSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	return "file:" + path + "?" + q.Encode()
}

// Open opens (creating if needed) the SQLite database at path and verifies it is reachable.
// sql.Open alone never touches the database; PingContext forces a real connection so that
// configuration mistakes fail here, at startup, rather than on the first query.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("sqldb: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqldb: ping %s: %w", path, err)
	}
	return db, nil
}

// Setup creates the tables and inserts the sample data (sql/schema.sql then sql/seed.sql).
// database/sql lets a single Exec contain many statements with this driver, which is
// convenient for scripts. Do not rely on that with other drivers (pgx wants one per call
// unless you use its simple protocol).
func Setup(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, sqlfiles.Schema); err != nil {
		return fmt.Errorf("sqldb: apply schema: %w", err)
	}
	if _, err := db.ExecContext(ctx, sqlfiles.Seed); err != nil {
		return fmt.Errorf("sqldb: apply seed: %w", err)
	}
	return nil
}

// Schema applies only the schema (no sample rows). Useful for tests that insert their own data.
func Schema(ctx context.Context, db *sql.DB) error {
	if _, err := db.ExecContext(ctx, sqlfiles.Schema); err != nil {
		return fmt.Errorf("sqldb: apply schema: %w", err)
	}
	return nil
}

// OpenTemp creates a fresh database file in a new temp directory, loads schema + seed, and
// returns the handle plus a cleanup function. Every lesson uses this so runs are repeatable:
// the previous run's data can never leak into the next one.
func OpenTemp(ctx context.Context) (*sql.DB, func(), error) {
	dir, err := os.MkdirTemp("", "sqlite-lesson-*")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() { os.RemoveAll(dir) }
	db, err := Open(ctx, filepath.Join(dir, "lesson.db"))
	if err != nil {
		cleanup()
		return nil, nil, err
	}
	if err := Setup(ctx, db); err != nil {
		db.Close()
		cleanup()
		return nil, nil, err
	}
	return db, func() { db.Close(); cleanup() }, nil
}
