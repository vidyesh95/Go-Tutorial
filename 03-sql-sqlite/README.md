# Track 03: SQL and SQLite with Go

Learn SQL and every common way Go talks to a database, using SQLite (pure Go driver
`modernc.org/sqlite`, no cgo, no server). Every lesson runs against a fresh temporary database,
so output is repeatable. The SQL is kept close to PostgreSQL; see
[`docs/postgres-later.md`](../docs/postgres-later.md) for what changes when you switch.

Domain: a tiny task tracker. `users 1-* projects 1-* tasks *-* tags` (`task_tags` is the join
table), plus an optional `tasks.assignee_id`. The canonical files are `sql/schema.sql` and
`sql/seed.sql`; `sql/embed.go` embeds them and `internal/sqldb` opens databases and loads them.

## Lessons

Run any lesson from the repo root with `go run ./03-sql-sqlite/<folder>`.

| Folder | Topic | You learn |
|--------|-------|-----------|
| `01basics` | SQL itself | INSERT / SELECT / UPDATE / DELETE, constraints as errors, NULL semantics |
| `02database-sql` | Go standard library | lazy `sql.Open`, Ping, Query/QueryRow/Exec, Scan, `rows.Err`, `sql.ErrNoRows`, `sql.Null[T]`, RETURNING, pool settings |
| `03prepared` | Safety | placeholders, prepared statements, SQL injection demo, whitelisting `ORDER BY` |
| `04transactions` | Atomicity | `BeginTx`, `defer tx.Rollback()`, a `withTx` helper, isolation levels |
| `05queries` | Reading data | joins, GROUP BY / HAVING, subqueries, CTEs, window functions, CASE, many-to-many |
| `06indexes` | Performance | `EXPLAIN QUERY PLAN`, scan vs search, composite index order, the N+1 problem |
| `07pagination` | APIs | OFFSET vs keyset pagination, opaque cursors |
| `08sqlx` | Less boilerplate | `Get`, `Select`, `NamedExec`, `sqlx.In`, struct tags |
| `09sqlc` | Type-safe codegen | `sqlc.yaml`, annotated queries, generated `db` package (committed), `WithTx` |
| `10migrations` | Schema changes | goose as a library with embedded migrations, Up/Down, expand/contract |
| `11testing-db` | Testing | a `store` package tested against a real temp-file database |

Run the tests: `go test -race ./03-sql-sqlite/...` (lesson 11 holds the tests; `sqldb` is shared).

Regenerate the sqlc code after editing `09sqlc/query.sql` or `sql/schema.sql`:

```shell
make sqlc        # runs: go tool sqlc generate -f 03-sql-sqlite/09sqlc/sqlc.yaml
```

sqlc and the goose CLI are pinned as Go tools in `go.mod` (`tool` directive), so there is nothing
to install. Using the goose CLI on a database file:

```shell
go tool goose -dir 03-sql-sqlite/10migrations/migrations sqlite3 ./app.db status
go tool goose -dir 03-sql-sqlite/10migrations/migrations sqlite3 ./app.db up
go tool goose -dir 03-sql-sqlite/10migrations/migrations sqlite3 ./app.db down
```

## Poking at the database by hand

The lessons delete their database on exit. To explore interactively, build one yourself
(the `sqlite3` shell ships with macOS and most Linux distributions):

```shell
sqlite3 /tmp/tasks.db < 03-sql-sqlite/sql/schema.sql
sqlite3 /tmp/tasks.db < 03-sql-sqlite/sql/seed.sql
sqlite3 /tmp/tasks.db
```

Useful shell commands:

```
.headers on            -- show column names
.mode column           -- aligned output (or .mode box / .mode json)
.tables                -- list tables
.schema tasks          -- show CREATE statement
PRAGMA foreign_keys = ON;   -- FKs are OFF by default in each new connection!
EXPLAIN QUERY PLAN SELECT * FROM tasks WHERE due_date = '2026-12-01';
.quit
```

## SQL cheat sheet

### Joins

```
A = users            B = tasks (assignee_id -> users.id)

INNER JOIN   only rows matching on both sides        A [###] B
LEFT JOIN    all of A, B columns NULL if no match    [A###] B
RIGHT JOIN   all of B (SQLite 3.39+; just swap the tables and use LEFT)
FULL JOIN    everything from both sides (SQLite 3.39+)
CROSS JOIN   every A row with every B row (cartesian product, rarely wanted)

Anti-join:   FROM a LEFT JOIN b ON ... WHERE b.id IS NULL     "A rows with no B"
```

Gotcha: `LEFT JOIN b ... WHERE b.x = 1` silently becomes an INNER JOIN. Put `b.x = 1` in the `ON`.

### Query order of evaluation

```
FROM/JOIN -> WHERE -> GROUP BY -> HAVING -> window functions -> SELECT -> DISTINCT -> ORDER BY -> LIMIT
```

That is why you cannot use a SELECT alias in WHERE, and why window functions only see rows that
survived WHERE.

### NULL rules

- `NULL = NULL` is NULL, not true. Use `IS NULL` / `IS NOT NULL` (or `IS NOT DISTINCT FROM` in PostgreSQL).
- `WHERE` keeps only rows where the condition is TRUE; NULL and FALSE are both dropped.
- `x NOT IN (1, NULL)` is never true. Use `NOT EXISTS` when the list can contain NULL.
- Aggregates ignore NULL (`COUNT(col)`, `SUM`, `AVG`); `COUNT(*)` counts rows.
- `COALESCE(a, b, 'default')` returns the first non-NULL.
- In Go: scan into `sql.Null[T]` or a pointer, never a bare `int`/`string`.

### Index rules

1. Index columns you filter, join, and sort on. Foreign keys are NOT indexed automatically.
2. Composite `(a, b)` serves `a`, and `a AND b`, but not `b` alone (leftmost prefix). Equality columns first, range/sort last.
3. A function on the column (`lower(email)`, `date(created_at)`) defeats a plain index; use an expression index.
4. `LIKE '%foo'` (leading wildcard) cannot use a B-tree index; `LIKE 'foo%'` can.
5. Every index slows writes and takes space. Drop indexes nothing uses.
6. Always check the plan: `EXPLAIN QUERY PLAN` (SQLite), `EXPLAIN (ANALYZE, BUFFERS)` (PostgreSQL).

## database/sql vs sqlx vs sqlc vs ORM

| | `database/sql` | `sqlx` | `sqlc` | ORM (GORM, ent, bun) |
|---|---|---|---|---|
| You write | SQL + Scan code | SQL + struct tags | SQL in `.sql` files | Go method chains / models |
| Type safety | none (runtime) | none (runtime) | compile time (checked at generate) | partial |
| Boilerplate | most | less | least | least |
| Dynamic queries | easy (string building) | easy | awkward (`sqlc.narg`, builders) | easy |
| Performance | baseline | about the same | same as `database/sql` | slower, hidden queries |
| Risks | typos, scan order bugs | typos, missing tags | stale generated code | N+1, surprising SQL, leaky abstraction |
| Best for | learning, small tools | pragmatic services | most new Go services | quick CRUD / prototypes |

Rule of thumb: learn `database/sql` first (everything builds on it), then use `sqlc` or `sqlx` in
real services. Know what an ORM generates before you trust one.

## Interview questions

1. **What are the transaction isolation levels?** They control which anomalies concurrent
   transactions can see. Read Uncommitted allows dirty reads; Read Committed (PostgreSQL default)
   prevents them; Repeatable Read also prevents non-repeatable reads (and phantoms in PostgreSQL);
   Serializable behaves as if transactions ran one at a time, at the cost of retries on conflict.
   SQLite is effectively serializable.

2. **What does ACID mean?** Atomicity (all or nothing), Consistency (constraints always hold),
   Isolation (concurrent transactions do not interfere), Durability (committed data survives a crash).

3. **What are the tradeoffs of an index?** Reads that filter/sort on the column get much faster
   (O(log n) vs O(n)); writes get slower, storage grows, and the planner can still ignore the
   index for low-selectivity filters. Index what queries need, verify with EXPLAIN, remove unused ones.

4. **What is the N+1 query problem?** One query for a list, then one per item for related data.
   Latency grows linearly with rows. Fix with a JOIN, a batched `WHERE id IN (...)`, or eager loading.

5. **Why use keyset pagination over OFFSET?** OFFSET reads and discards all skipped rows (slow for
   deep pages) and shifts when rows are inserted/deleted (duplicates or skips). Keyset uses
   `WHERE (sort_col, id) > (last_values)` with an index, so it is fast and stable, but has no random page access.

6. **How do you prevent SQL injection?** Use parameterized queries (placeholders), never string
   concatenation. Whitelist identifiers such as dynamic ORDER BY columns. Use least-privilege database users.

7. **How do transactions work in Go?** `tx, _ := db.BeginTx(ctx, nil)`, `defer tx.Rollback()`
   (a no-op after commit), run every statement on `tx`, then `tx.Commit()`. Wrap it in a
   `withTx(ctx, db, func(tx) error)` helper so callers cannot forget. Keep them short; retry on serialization failures.

8. **How does the connection pool work and how do you tune it?** `*sql.DB` is a pool of
   connections, safe for concurrent use. `SetMaxOpenConns` caps load on the database,
   `SetMaxIdleConns` keeps warm connections, `SetConnMaxLifetime` recycles them. Total
   across all instances must stay under the database's connection limit. Leaked `rows`
   (missing `Close`) exhaust the pool.

9. **What is normalization, and when do you denormalize?** Normalization stores each fact once
   (1NF atomic values, 2NF/3NF no partial or transitive dependencies) to avoid update anomalies.
   Denormalize deliberately for read performance (counters, reporting tables) and accept the
   cost of keeping copies in sync.

10. **How do you run migrations safely?** Versioned, forward-only files in source control, run
    once from a single place before/while deploying; never edit applied migrations. For zero
    downtime use expand/contract: add (nullable) -> dual write -> backfill -> switch reads -> drop later.

11. **What is optimistic locking?** Add a `version` column. Read it with the row, then
    `UPDATE ... SET ..., version = version + 1 WHERE id = ? AND version = ?`. If `RowsAffected` is 0,
    someone else changed the row: reload and retry or report a conflict. No locks are held while
    the user thinks. (Pessimistic locking: `SELECT ... FOR UPDATE` in PostgreSQL.)

12. **What is a deadlock and how do you avoid it?** Two transactions each hold a lock the other
    needs and wait forever; the database kills one with an error. Avoid by locking rows in a
    consistent order, keeping transactions short, and retrying the aborted transaction.
    (SQLite avoids it with one writer at a time but can return "database is locked"; set `busy_timeout`.)
