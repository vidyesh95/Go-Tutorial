// Package sqlitedb opens a SQLite database with the settings every service in this track uses.
//
// Each service owns its own database file: users-svc has users.db, orders-svc has orders.db,
// and neither ever opens the other's. That is the "database per service" rule, and it is the
// single most important boundary in a microservice system (see README).
package sqlitedb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib" // result-code constants
)

// DSN builds the connection string. The pragmas run on EVERY connection the pool opens, because
// they are per-connection settings in SQLite:
//   - foreign_keys(1): off by default, REFERENCES does nothing without it.
//   - journal_mode(WAL): readers do not block the writer.
//   - busy_timeout(5000): a blocked writer waits up to 5s instead of failing instantly.
//   - _txlock=immediate: transactions take the write lock up front, so a read-then-write
//     transaction cannot fail halfway with SQLITE_BUSY while upgrading its lock.
func DSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Open opens the database file (creating it if needed) and applies schema, which should be
// idempotent (CREATE TABLE IF NOT EXISTS). Ping forces a real connection, so a bad path fails
// at startup instead of on the first request. A real project would use migrations (see
// 05-projects/taskapi); a one-shot schema keeps this track focused on the service concepts.
func Open(ctx context.Context, path, schema string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("sqlitedb: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitedb: ping %s: %w", path, err)
	}
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlitedb: apply schema: %w", err)
	}
	return db, nil
}

// IsUniqueViolation reports whether err is a UNIQUE or PRIMARY KEY constraint failure. Callers
// translate it into their own domain error ("already exists") so SQL details never leak out.
func IsUniqueViolation(err error) bool {
	se, ok := errors.AsType[*sqlite.Error](err)
	if !ok {
		return false
	}
	switch se.Code() {
	case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
		return true
	}
	return false
}
