# Interview prep: Go, backend, SQL

Short answers to questions that come up again and again in Go backend interviews. Each track's
README has more detailed questions; this page is the cross-cutting cheat sheet. Say the answer out
loud, then open the linked lesson and check you could write the code.

## Go language

**Array vs slice?** An array has a fixed length that is part of its type (`[3]int`) and is copied by
value. A slice is a small header (pointer, len, cap) over an array. Copying a slice shares the
backing array. → `01-fundamentals/10arrays`, `11slices`

**What happens on `append` when cap is full?** A new, larger array is allocated and elements are
copied. Other slices still point at the old array — a classic aliasing bug. → `11slices`

**Are maps safe for concurrent use?** No. Concurrent writes crash the program ("concurrent map
writes"). Use a `sync.Mutex`/`RWMutex`, or `sync.Map` for specific read-heavy cases. → `02-concurrency/04sync`

**Value vs pointer receiver?** Pointer receiver when the method mutates, the struct is large, or it
contains a mutex. Be consistent across a type's methods. → `16methods`

**How are interfaces satisfied?** Implicitly — any type with the methods satisfies it. Keep
interfaces small and define them where they're *used*. "Accept interfaces, return structs." → `17interfaces`

**Why is `err != nil` true when I returned a nil `*MyErr`?** An interface is (type, value). A typed
nil pointer gives (`*MyErr`, nil), which is not a nil interface. Return a literal `nil`. → `17interfaces`, `20errors`

**How do you handle errors?** Return them, wrap with context (`fmt.Errorf("load user %d: %w", id, err)`),
check with `errors.Is` (sentinel) / `errors.As` or `errors.AsType` (type). Handle each error once:
log *or* return, not both. → `20errors`

**When is `panic` acceptable?** Programmer errors / impossible states and init-time failures. Never
for expected failures like bad input or a missing row. → `21defer`

**When should you use generics?** When the same algorithm works over many types (collections,
`Map/Filter`, data structures). Not for things interfaces already express well. → `23generics`

**What changed with loop variables in Go 1.22?** Each iteration gets a new variable, so closures and
goroutines capturing it no longer see the last value. → `30modern-go`

## Concurrency

**Goroutine vs OS thread?** Goroutines are scheduled by the Go runtime onto a few OS threads (M:N),
start with a tiny stack (~2 KB) that grows, and are cheap to create (100k+ is normal).

**Unbuffered vs buffered channel?** Unbuffered: send blocks until a receiver takes it (synchronizes).
Buffered: send blocks only when the buffer is full. → `02-concurrency/02channels`

**Mutex or channel?** Mutex to protect shared state; channels to pass ownership of data or signal
events. "Share memory by communicating" is a guideline, not a rule. → `02-concurrency/README.md`

**What is a goroutine leak and how do you prevent it?** A goroutine blocked forever (e.g. sending on
a channel no one reads). Always give goroutines a way to exit: `ctx.Done()`, closing channels,
buffered result channels. → `13leaks`

**What is `context` for?** Cancellation, deadlines and request-scoped values across API boundaries.
Pass it as the first parameter; never store it in a struct. → `06context`

**How do you detect data races?** `go test -race` / `go run -race`. Run it in CI. → `11race`

**errgroup?** Runs goroutines, returns the first error, and cancels the shared context so the others
stop. `SetLimit` bounds concurrency. → `09errgroup`

## HTTP / backend

**What timeouts does a production `http.Server` need?** `ReadHeaderTimeout` (Slowloris),
`ReadTimeout`, `WriteTimeout`, `IdleTimeout`. Plus client `Timeout` on outgoing calls. → `04-web-stdlib/01server`

**How does graceful shutdown work?** Catch SIGTERM with `signal.NotifyContext`, mark not-ready,
call `srv.Shutdown(ctx)` which stops accepting and waits for in-flight requests, then close the DB. → `06graceful`

**How do you structure a Go service?** `cmd/<app>/main.go` for wiring; `internal/` for handler →
service → store layers; interfaces defined by the consumer; dependencies injected through structs;
no globals. → `04-web-stdlib/08structure`, `05-projects/taskapi`

**PUT vs PATCH, and which methods are idempotent?** PUT replaces the whole resource, PATCH applies
a partial update. GET, PUT, DELETE (and HEAD, OPTIONS) are idempotent; POST is not, unless you use
an `Idempotency-Key`.

**401 vs 403 vs 404?** 401 = not authenticated. 403 = authenticated but not allowed. Return 404 for
another user's private resource so you don't reveal it exists.

**How do JWTs work, and their pitfalls?** A signed (not encrypted) token with claims (`sub`, `exp`).
The server verifies the signature and expiry, so no session lookup is needed. Pitfalls: always pin
the algorithm, keep TTLs short, revoking tokens is hard (use refresh tokens and a denylist), never put
secrets in claims. → `05-projects/taskapi/internal/auth`

**How do you rate limit?** A token bucket per client (`x/time/rate`); for multiple instances use a
shared store (Redis). Return 429 with `Retry-After`. → `02-concurrency/10ratelimit`, taskapi middleware

**How do you make retries safe?** Retry only idempotent operations or ones with an idempotency key,
use exponential backoff with jitter, cap the attempts, and respect `Retry-After`. → `04-web-stdlib/05client`

**Monolith vs microservices?** Start with a modular monolith. Split when teams, scaling needs, or
deploy cadence demand it. Microservices add network failures, distributed transactions, and
observability costs. → `07-microservices/README.md`

## SQL

**ACID?** Atomicity (all or nothing), Consistency (constraints hold), Isolation (concurrent
transactions don't interfere beyond the isolation level), Durability (committed data survives a crash).

**Isolation levels?** Read Uncommitted < Read Committed (Postgres default) < Repeatable Read <
Serializable. Higher levels prevent more anomalies (dirty reads, non-repeatable reads, phantoms)
at the cost of contention and retries.

**How do transactions look in Go?** `tx, err := db.BeginTx(ctx, nil)`; `defer tx.Rollback()`
(it's a no-op after a commit); do the work with `tx`; `return tx.Commit()`. → `03-sql-sqlite/04transactions`

**INNER vs LEFT JOIN?** INNER keeps only rows that match on both sides. LEFT keeps every left row,
with NULLs where there is no match. → `05queries`

**What is an index and what does it cost?** A B-tree that makes lookups O(log n) instead of a full
scan. It costs disk space and slower writes. Composite index column order matters (leftmost
prefix rule). → `06indexes`

**N+1 query problem?** One query for a list, then one query per item. Fix it with a JOIN or a
single `WHERE id IN (...)` batch query. → `06indexes`

**OFFSET vs keyset pagination?** OFFSET scans and discards rows (slow on deep pages) and skips or
duplicates rows when data changes. Keyset (`WHERE (k, id) > (?, ?) ORDER BY k, id LIMIT n`) uses
an index and is stable. → `07pagination`

**How do you prevent SQL injection?** Always use placeholders (`?` / `$1`). Never build SQL from
user input with string concatenation. Whitelist dynamic identifiers like ORDER BY columns. → `03prepared`

**Why sqlc/sqlx over an ORM?** You see and control the exact SQL, there is no magic or hidden N+1,
and you get type safety from generated code (sqlc). → `docs/industry-stack.md`

**Optimistic vs pessimistic locking?** Optimistic: a `version` column, `UPDATE ... WHERE id=? AND
version=?`, and a retry when 0 rows were affected. Pessimistic: `SELECT ... FOR UPDATE` (Postgres)
holds a row lock.

**Normalization?** Store each fact once (to roughly 3NF) to avoid update anomalies. Denormalize
deliberately for read performance.

**How do you migrate safely with zero downtime?** Expand then contract: add the new nullable column,
deploy code that writes both, backfill, switch reads, then drop the old column later. Never edit a
migration that has already been applied. → `10migrations`

## System design starters

Be ready to sketch these at a whiteboard:
- URL shortener (ID generation, caching, redirects)
- Rate limiter (token bucket, Redis, distributed counters)
- Job queue / worker (at-least-once delivery, retries, idempotency, dead-letter) → `08-use-cases`
- Chat (WebSockets, fan-out, presence) → `08-use-cases`
- Order service (sagas, outbox pattern) → `07-microservices`
