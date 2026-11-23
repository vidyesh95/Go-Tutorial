// Lesson 10: schema migrations with goose.
//
// A migration is a numbered, versioned SQL file that changes the schema one step. The database
// remembers which versions ran (goose keeps them in a goose_db_version table), so "migrate up"
// applies only what is missing. This makes every environment (laptop, CI, staging, prod) reach
// the same schema by running the same files in order.
//
// File format (see migrations/):
//
//	-- +goose Up
//	CREATE TABLE ...;
//	-- +goose Down
//	DROP TABLE ...;
//
// Here goose is used as a LIBRARY: the SQL files are embedded into the binary (go:embed), and the
// app migrates itself at startup. goose's Provider API (goose.NewProvider) is the recommended
// modern API: it takes the *sql.DB and an fs.FS explicitly, has no global state (the older
// goose.SetDialect / SetBaseFS / Up functions mutate package globals), and returns structured
// results. The same migrations can also be run from the CLI (goose is added to go.mod as a tool):
//
//	go tool goose -dir 03-sql-sqlite/10migrations/migrations sqlite3 ./app.db status
//	go tool goose -dir 03-sql-sqlite/10migrations/migrations sqlite3 ./app.db up
//	go tool goose -dir 03-sql-sqlite/10migrations/migrations sqlite3 ./app.db down
//	go tool goose -dir 03-sql-sqlite/10migrations/migrations create add_tasks sql
//
// Rules that keep teams out of trouble:
//   - NEVER edit a migration that has been applied anywhere shared. Write a new migration that
//     fixes forward. (Editing history makes environments diverge silently.)
//   - Keep each migration small and focused; one logical change per file.
//   - Test the Down too, but know that production usually rolls FORWARD; a Down that drops a
//     column destroys data.
//   - Run migrations from one place (a deploy step or one instance), not from every replica at once.
//
// Zero-downtime changes use EXPAND / CONTRACT. Example: rename users.name to users.full_name.
//  1. Expand:   ADD COLUMN full_name (nullable). Deploy code that writes BOTH columns.
//  2. Migrate:  backfill full_name from name in small batches.
//  3. Switch:   deploy code that reads full_name only.
//  4. Contract: later migration DROPs the old column once no running code uses it.
//
// Each step is backwards compatible with the code version running at that moment, so old and new
// application versions can run side by side during a rolling deploy. A single "RENAME COLUMN"
// would break the old version the instant it ran. Also beware long locks: adding a NOT NULL column
// with a default or building an index can block writes on large PostgreSQL tables
// (use CREATE INDEX CONCURRENTLY there).
//
// In industry: every serious service has migrations in version control, run by CI/CD before
// the new code rolls out (goose, golang-migrate, Atlas, or Flyway). sqlc can read the migrations
// directory directly as its schema source.
package main

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"

	"github.com/pressly/goose/v3"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

//go:embed migrations/*.sql
var embedded embed.FS

func main() {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "goose-lesson-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	// A fresh, EMPTY database (not sqldb.Setup): migrations are what create the schema.
	db, err := sqldb.Open(ctx, filepath.Join(dir, "app.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	// fs.Sub strips the "migrations/" prefix so goose sees the .sql files at the root.
	migrationsFS, err := fs.Sub(embedded, "migrations")
	if err != nil {
		log.Fatal(err)
	}
	// DialectSQLite3 is goose's name for SQLite (the driver we use is modernc "sqlite").
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, migrationsFS)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("== Status before anything is applied ==")
	printStatus(ctx, provider)

	fmt.Println("\n== Up: apply all pending migrations ==")
	results, err := provider.Up(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range results {
		fmt.Printf("applied %05d %s\n", r.Source.Version, filepath.Base(r.Source.Path))
	}
	printVersion(ctx, provider)
	printStatus(ctx, provider)
	fmt.Println("tables and indexes now in the database:")
	printTables(ctx, db)

	fmt.Println("\n== Down: roll back ONE step (00003 only) ==")
	res, err := provider.Down(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("rolled back %05d %s\n", res.Source.Version, filepath.Base(res.Source.Path))
	printVersion(ctx, provider)
	printStatus(ctx, provider)

	fmt.Println("\n== Up again ==")
	results, err = provider.Up(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, r := range results {
		fmt.Printf("applied %05d %s\n", r.Source.Version, filepath.Base(r.Source.Path))
	}
	printVersion(ctx, provider)

	fmt.Println("\n== Up when nothing is pending is a safe no-op (idempotent) ==")
	results, err = provider.Up(ctx)
	fmt.Println("migrations applied:", len(results), "err:", err)
	pending, _ := provider.HasPending(ctx)
	fmt.Println("has pending:", pending)
}

func printStatus(ctx context.Context, p *goose.Provider) {
	statuses, err := p.Status(ctx)
	if err != nil {
		log.Fatal(err)
	}
	for _, s := range statuses {
		applied := "-"
		if !s.AppliedAt.IsZero() {
			applied = s.AppliedAt.UTC().Format("15:04:05")
		}
		fmt.Printf("  %-8s %-26s applied_at=%s\n", s.State, filepath.Base(s.Source.Path), applied)
	}
}

func printVersion(ctx context.Context, p *goose.Provider) {
	v, err := p.GetDBVersion(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("current schema version:", v)
}

// printTables lists user tables and indexes so you can see what the migrations created.
func printTables(ctx context.Context, db *sql.DB) {
	rows, err := db.QueryContext(ctx, `SELECT type, name FROM sqlite_master
WHERE name NOT LIKE 'sqlite_%' AND name NOT LIKE '%_autoindex_%' ORDER BY type DESC, name`)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var typ, name string
		if err := rows.Scan(&typ, &name); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("  %-6s %s\n", typ, name)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
}
