package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"io/fs"

	"github.com/pressly/goose/v3"
)

// The SQL files are compiled into the binary, so the container needs no migrations directory
// and the binary can never be deployed with a schema it does not understand.
//
//go:embed migrations/*.sql
var migrationsFS embed.FS

// Migrate applies every pending migration. It uses goose's Provider API: explicit *sql.DB and
// fs.FS arguments, no package-level state, structured results.
//
// Run it from ONE place (the deploy step, or this process at startup when there is a single
// instance, as here). Several replicas migrating simultaneously is a classic outage. goose
// takes a lock on PostgreSQL, but the safe habit is "one migrator".
func Migrate(ctx context.Context, db *sql.DB) error {
	// fs.Sub strips the "migrations/" prefix so goose sees the .sql files at the root.
	sub, err := fs.Sub(migrationsFS, "migrations")
	if err != nil {
		return fmt.Errorf("store: migrations fs: %w", err)
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, db, sub)
	if err != nil {
		return fmt.Errorf("store: new migration provider: %w", err)
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("store: apply migrations: %w", err)
	}
	return nil
}
