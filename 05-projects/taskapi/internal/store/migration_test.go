package store

import (
	"context"
	"database/sql"
	"io/fs"
	"path/filepath"
	"testing"

	"github.com/pressly/goose/v3"
)

// TestMigration00002MovesPasswordsToAccounts exercises the expand/contract data migration: a
// database that lived at version 1 (users.password_hash) is upgraded, and the data must survive.
func TestMigration00002MovesPasswordsToAccounts(t *testing.T) {
	ctx := t.Context()
	db, err := Open(ctx, filepath.Join(t.TempDir(), "mig.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		t.Fatal(err)
	}
	p, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		t.Fatal(err)
	}

	// --- Version 1 (the old schema) with real data ---------------------------------------------
	if _, err := p.UpTo(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if !hasColumn(t, db, "users", "password_hash") {
		t.Fatal("version 1 should have users.password_hash")
	}
	exec(t, db, `INSERT INTO users (id, email, name, password_hash, created_at) VALUES
		('u1', 'ada@example.com', 'Ada', '$2a$12$legacybcrypt', '2026-01-02T03:04:05.000Z'),
		('u2', 'bob@example.com', 'Bob', '$2a$12$otherhash', '2026-02-03T04:05:06.000Z')`)
	exec(t, db, `INSERT INTO projects (id, owner_id, name, created_at, updated_at) VALUES ('p1', 'u1', 'Keep me', 'x', 'x')`)

	// --- Up to version 2 --------------------------------------------------------------------------
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatal(err)
	}
	if hasColumn(t, db, "users", "password_hash") {
		t.Error("password_hash must be dropped")
	}
	var verified int
	var image sql.NullString
	var created, updated string
	if err := db.QueryRowContext(ctx, `SELECT email_verified, image, created_at, updated_at FROM users WHERE id='u1'`).Scan(&verified, &image, &created, &updated); err != nil {
		t.Fatal(err)
	}
	if verified != 0 || image.Valid || updated != created || created != "2026-01-02T03:04:05.000Z" {
		t.Errorf("users row after migration: verified=%d image=%v created=%q updated=%q", verified, image, created, updated)
	}

	var providerID, accountID, password string
	if err := db.QueryRowContext(ctx, `SELECT provider_id, account_id, password FROM account WHERE user_id='u1'`).Scan(&providerID, &accountID, &password); err != nil {
		t.Fatalf("no account row for u1: %v", err)
	}
	if providerID != "credential" || accountID != "u1" || password != "$2a$12$legacybcrypt" {
		t.Errorf("account = %q %q %q", providerID, accountID, password)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM account`); n != 2 {
		t.Errorf("account rows = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM projects`); n != 1 {
		t.Errorf("projects lost during migration: %d", n)
	}
	for _, table := range []string{"session", "verification"} {
		if n := count(t, db, `SELECT COUNT(*) FROM `+table); n != 0 {
			t.Errorf("%s should start empty", table)
		}
	}

	// --- Down to version 1 --------------------------------------------------------------------------
	exec(t, db, `INSERT INTO users (id, email, name, created_at, updated_at) VALUES ('u3', 'gh@example.com', 'GH', 'x', 'x')`) // no credential account
	if _, err := p.DownTo(ctx, 1); err != nil {
		t.Fatalf("down: %v", err)
	}
	for _, table := range []string{"account", "session", "verification"} {
		if hasTable(t, db, table) {
			t.Errorf("table %s survived Down", table)
		}
	}
	for _, col := range []string{"email_verified", "image", "updated_at"} {
		if hasColumn(t, db, "users", col) {
			t.Errorf("column users.%s survived Down", col)
		}
	}
	var hash string
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id='u1'`).Scan(&hash); err != nil || hash != "$2a$12$legacybcrypt" {
		t.Errorf("password_hash after Down = %q, %v", hash, err)
	}
	if err := db.QueryRowContext(ctx, `SELECT password_hash FROM users WHERE id='u3'`).Scan(&hash); err != nil || hash != "" {
		t.Errorf("oauth-only user's password_hash after Down = %q, %v; want empty", hash, err)
	}

	// And forward again: the migration is re-runnable.
	if _, err := p.UpTo(ctx, 2); err != nil {
		t.Fatalf("re-apply: %v", err)
	}
}

func exec(t *testing.T, db *sql.DB, q string) {
	t.Helper()
	if _, err := db.ExecContext(context.Background(), q); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
}

func count(t *testing.T, db *sql.DB, q string) int {
	t.Helper()
	var n int
	if err := db.QueryRowContext(context.Background(), q).Scan(&n); err != nil {
		t.Fatalf("%s: %v", q, err)
	}
	return n
}

func hasColumn(t *testing.T, db *sql.DB, table, column string) bool {
	t.Helper()
	return count(t, db, `SELECT COUNT(*) FROM pragma_table_info('`+table+`') WHERE name='`+column+`'`) == 1
}

func hasTable(t *testing.T, db *sql.DB, table string) bool {
	t.Helper()
	return count(t, db, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='`+table+`'`) == 1
}
