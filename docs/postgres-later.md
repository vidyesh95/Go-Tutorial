# Moving from SQLite to PostgreSQL

This repo uses SQLite so you can learn with zero setup. Most Go jobs use PostgreSQL. The good
news: if you understand the SQL track, switching is mostly mechanical. This page lists what changes.

## 1. Run PostgreSQL locally

```yaml
# docker-compose.yml
services:
  db:
    image: postgres:18
    environment:
      POSTGRES_USER: app
      POSTGRES_PASSWORD: app
      POSTGRES_DB: app
    ports: ["5432:5432"]
    volumes: ["pgdata:/var/lib/postgresql"]
volumes:
  pgdata:
```

```shell
docker compose up -d
docker compose exec db psql -U app      # interactive SQL shell
```

## 2. Swap the driver

| | SQLite (this repo) | PostgreSQL |
|---|---|---|
| Driver import | `_ "modernc.org/sqlite"` | `_ "github.com/jackc/pgx/v5/stdlib"` (database/sql) or `github.com/jackc/pgx/v5/pgxpool` (native, faster) |
| `sql.Open` name | `"sqlite"` | `"pgx"` |
| DSN | `file:app.db?_pragma=foreign_keys(1)` | `postgres://app:app@localhost:5432/app?sslmode=disable` |
| sqlc engine | `engine: "sqlite"` | `engine: "postgresql"`, `sql_package: "pgx/v5"` |
| goose dialect | `goose.SetDialect("sqlite3")` | `goose.SetDialect("postgres")` |

Keep the DSN in an environment variable (`DATABASE_URL`) so code doesn't change between environments.

## 3. SQL dialect differences you will hit

| Topic | SQLite | PostgreSQL |
|-------|--------|------------|
| Placeholders | `?` | `$1, $2, ...` (sqlx `Rebind` converts; sqlc handles it) |
| Auto-increment id | `INTEGER PRIMARY KEY` | `BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY` |
| UUID ids | `TEXT` | `UUID` type (`uuid.NewV7()` from Go 1.27 stdlib works for both) |
| Timestamps | `TEXT` ISO-8601 | `TIMESTAMPTZ` (scans straight into `time.Time`) |
| Booleans | `INTEGER` 0/1 | `BOOLEAN` |
| Now | `strftime('%Y-%m-%dT%H:%M:%SZ','now')` | `now()` |
| Enums | `CHECK (status IN (...))` | same CHECK, or `CREATE TYPE ... AS ENUM` |
| Get inserted id | `RETURNING id` (3.35+) or `LastInsertId()` | `RETURNING id` (`LastInsertId` not supported) |
| Upsert | `ON CONFLICT (...) DO UPDATE` | same |
| Foreign keys | must enable per connection | always on |
| Case-insensitive search | `LIKE` is case-insensitive for ASCII | `ILIKE` |
| JSON | `json_extract(col, '$.a')` | `JSONB` + `col->>'a'`, GIN indexes |
| Concurrency | one writer at a time (WAL helps) | many concurrent writers (MVCC) |
| Isolation levels | serializable only | Read Committed default; Repeatable Read, Serializable |

This repo already uses `RETURNING`, `ON CONFLICT`, and CHECK constraints so most queries port unchanged.

## 4. Things PostgreSQL adds that are worth learning next

- `EXPLAIN ANALYZE` and reading query plans (seq scan vs index scan)
- Index types: B-tree, GIN (JSONB, full-text, arrays), partial and expression indexes
- `SELECT ... FOR UPDATE` / `SKIP LOCKED` — row locking, building job queues in Postgres
- `LISTEN/NOTIFY`, materialized views, `COPY` for bulk loads (`pgx.CopyFrom`)
- Connection pooling (`pgxpool`, PgBouncer) and `max_connections`
- Integration tests with `testcontainers-go` (real Postgres in Docker per test run)

## 5. Checklist to port `05-projects/taskapi`

1. Add the compose file above; set `DATABASE_URL`.
2. Change migration SQL types (id, timestamps, booleans) per the table.
3. Change `sqlc.yaml` engine to `postgresql`, regenerate with `go tool sqlc generate`.
4. Replace driver import + `sql.Open` name (or move to `pgxpool`).
5. Map Postgres errors: unique violation is SQLSTATE `23505` (`*pgconn.PgError`), FK violation `23503`.
6. Run tests against the container.
