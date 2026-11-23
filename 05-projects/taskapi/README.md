# taskapi - capstone REST API

A production-style task-tracking API in Go: users own **projects**, projects contain **tasks**.
Standard-library routing, SQLite with type-safe SQL (sqlc) and embedded migrations (goose), bcrypt +
JWT authentication, structured logging, Prometheus metrics, per-IP rate limiting, graceful shutdown,
a distroless Docker image, and tests at every layer.

It deliberately uses very few dependencies, so you can explain every line of it in an interview.

| Concern | Choice |
|---|---|
| HTTP | `net/http` + Go 1.22 `ServeMux` patterns (`GET /v1/tasks/{id}`) |
| Database | SQLite (pure Go driver `modernc.org/sqlite`, no cgo), WAL mode |
| SQL | [sqlc](https://sqlc.dev) generates Go from `queries/*.sql`; no ORM |
| Migrations | [goose](https://github.com/pressly/goose) Provider API, files embedded in the binary |
| Auth | bcrypt (cost 12) passwords, HS256 JWT bearer tokens (`golang-jwt/jwt/v5`) |
| IDs | UUIDv7 from the standard library `uuid` package (time-ordered) |
| Logging | `log/slog`, JSON or text |
| Metrics | `prometheus/client_golang` on `/metrics` |
| Rate limit | `golang.org/x/time/rate`, one token bucket per client IP |
| Tests | table tests + fakes (service), real SQLite (store, HTTP), `httptest.NewTestServer`, `-race` |

## Run it

```sh
# from the repository root
export TASKAPI_JWT_SECRET="$(openssl rand -base64 48)"   # required, >= 32 bytes
go run ./05-projects/taskapi/cmd/api                      # listens on :8080, creates ./taskapi.db
```

Stop it with Ctrl-C: it finishes in-flight requests before exiting ("graceful shutdown").

```sh
go test -race ./05-projects/...                           # all tests
make sqlc                                                 # regenerate internal/store/sqlcdb after editing SQL
go run ./05-projects/taskapi/cmd/api -migrate-only        # apply migrations and exit (deploy step)

docker build -f 05-projects/taskapi/Dockerfile -t taskapi .      # context = repo root (where go.mod is)
docker run --rm -p 8080:8080 -e TASKAPI_JWT_SECRET="$(openssl rand -base64 48)" \
  -v taskapi-data:/data taskapi
```

### Configuration (environment variables)

| Variable | Default | Meaning |
|---|---|---|
| `TASKAPI_JWT_SECRET` | **required** | HMAC signing key, at least 32 bytes. Never logged (redacted). |
| `TASKAPI_ADDR` | `:8080` | Listen address |
| `TASKAPI_DB_PATH` | `taskapi.db` | SQLite file (`/data/taskapi.db` in the Docker image) |
| `TASKAPI_JWT_TTL` | `24h` | Token lifetime |
| `TASKAPI_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, `error` |
| `TASKAPI_LOG_FORMAT` | `json` | `json` for machines, `text` for terminals |
| `TASKAPI_RATE_RPS` | `10` | Sustained requests/second allowed per client IP |
| `TASKAPI_RATE_BURST` | `20` | Burst size per client IP |
| `TASKAPI_CORS_ORIGINS` | empty (CORS off) | Comma-separated allowed browser origins |
| `TASKAPI_SHUTDOWN_TIMEOUT` | `15s` | Max wait for in-flight requests on SIGINT/SIGTERM |

See `.env.example`. Bad configuration fails at startup with every problem listed at once.

## Architecture

Dependencies point **inward**: each layer knows only the layer below it, and the `domain` package
knows nothing about anyone.

```
                  cmd/api/main.go        wiring only: config -> logger -> db -> migrate -> build -> serve
                         |
                         v
   +-------------------------------------------------------------+
   |  internal/httpapi    routing, middleware, JSON, error->status |   HTTP concerns only
   +-------------------------------------------------------------+
                         |  calls
                         v
   +-------------------------------------------------------------+
   |  internal/service    validation, authorization, orchestration |   business rules
   |   declares the small interfaces it needs: UserRepo,           |
   |   ProjectRepo, TaskRepo  (consumer-defined)                   |
   +-------------------------------------------------------------+
        |  uses                                  ^  implemented by
        v                                        |
   internal/auth                      +--------------------------------+
   bcrypt + JWT                       |  internal/store                |   SQL + error translation
                                      |   store.go  -> sqlcdb (generated)
                                      |   migrations/*.sql (embedded)  |
                                      +--------------------------------+
                                                   |
                                                SQLite file

   internal/domain   entities, input validation, sentinel errors   (imported by everyone, imports nothing)
   internal/config   env vars -> validated Config
```

Why this shape: the service can be unit-tested with in-memory fakes (no database, no HTTP), the
store can be tested against real SQLite (no HTTP), and the HTTP layer can be tested end to end. Swapping
SQLite for PostgreSQL means writing a new `store` and changing one wiring line.

### Request flow

```
client
  |  POST /v1/projects/{id}/tasks   Authorization: Bearer <jwt>
  v
requestID        assign/accept X-Request-ID, put it in the context
accessLog        (after the response) one slog line: method, route, status, duration, request_id
metrics          (after the response) http_requests_total / http_request_duration_seconds
recoverer        panic -> logged stack trace + clean 500
cors             answer browser preflights, set Access-Control-* for allowed origins
rateLimit        per-IP token bucket; 429 + Retry-After when empty
ServeMux         match "POST /v1/projects/{id}/tasks"; sets r.Pattern; 404/405 for the rest
requireAuth      verify JWT (signature, HS256 only, issuer, expiry) -> user id in context
handler          decodeJSON (1 MiB cap, unknown fields rejected) -> service call -> writeJSON
service          validate input; prove the caller owns the project; build the Task (uuid v7, UTC time)
store            INSERT via sqlc (parameterized); map driver errors to domain errors
  |
  v  any error travels back up, wrapped with context at each layer ("create task: store: create task: ...")
mapError         the ONE place that turns domain errors into status codes + the JSON error envelope
```

### Data model

```
users(id, email UNIQUE, name, password_hash, created_at)
  1 --- * projects(id, owner_id -> users, name, UNIQUE(owner_id, name), created_at, updated_at)
            1 --- * tasks(id, project_id -> projects, title, status todo|doing|done,
                          priority 1..5, due_date, created_at, updated_at)
```

Deleting a user or project cascades (`ON DELETE CASCADE`, enforced because every connection enables
`foreign_keys`).

## API overview

Full machine-readable spec: [`api/openapi.yaml`](api/openapi.yaml) (OpenAPI 3.1).

| Method & path | Auth | Description |
|---|---|---|
| `POST /v1/auth/signup` | no | `{email, name, password}` -> 201 user |
| `POST /v1/auth/login` | no | `{email, password}` -> `{token, expires_at}` |
| `GET /v1/me` | yes | current user |
| `GET /v1/projects` | yes | my projects (cursor pagination) |
| `POST /v1/projects` | yes | `{name}` -> 201 (409 if the name is taken) |
| `GET/PATCH/DELETE /v1/projects/{id}` | yes | read, rename, delete (cascades to tasks) |
| `GET /v1/projects/{id}/tasks` | yes | `?status=todo\|doing\|done&cursor=&limit=` |
| `POST /v1/projects/{id}/tasks` | yes | `{title, priority?, due_date?}` -> 201 + `Location` |
| `GET/PATCH/DELETE /v1/tasks/{id}` | yes | read, partial update, delete |
| `GET /healthz` `/readyz` `/metrics` | no | liveness, readiness (503 while draining), Prometheus |

Errors always look like
`{"error": {"code": "validation_failed", "message": "...", "fields": {"title": "..."}, "request_id": "..."}}`.
Statuses: 400 malformed JSON, 401 bad/missing token, 404 not found *or not yours*, 409 duplicate,
413 body over 1 MiB, 422 invalid values, 429 rate limited, 500 server fault (details only in the log).

## curl walkthrough

Start the server in one terminal (see **Run it**), then in another:

```sh
BASE=http://localhost:8080
JSON='Content-Type: application/json'

# 1. Sign up. The response never contains the password or its hash.
curl -s -X POST $BASE/v1/auth/signup -H "$JSON" \
  -d '{"email":"ada@example.com","name":"Ada","password":"correct horse battery"}'

# 2. Log in and keep the token in a shell variable.
curl -s -X POST $BASE/v1/auth/login -H "$JSON" \
  -d '{"email":"ada@example.com","password":"correct horse battery"}'
export TOKEN=<paste the "token" value here>
# one-liner alternative (needs jq):
# export TOKEN=$(curl -s -X POST $BASE/v1/auth/login -H "$JSON" \
#   -d '{"email":"ada@example.com","password":"correct horse battery"}' | jq -r .token)

AUTH="Authorization: Bearer $TOKEN"
curl -s $BASE/v1/me -H "$AUTH"

# 3. Create a project (note the 201 and the Location header with -i).
curl -s -i -X POST $BASE/v1/projects -H "$AUTH" -H "$JSON" -d '{"name":"Website relaunch"}'
export PID=<paste the project "id" here>

# 4. Create tasks. priority (1-5, default 3) and due_date (YYYY-MM-DD) are optional.
for t in "Write copy" "Design hero" "Set up CI" "Pick fonts" "Ship it"; do
  curl -s -X POST $BASE/v1/projects/$PID/tasks -H "$AUTH" -H "$JSON" \
    -d "{\"title\":\"$t\",\"priority\":2,\"due_date\":\"2026-12-01\"}"; echo
done

# 5. List with cursor pagination: 2 per page. "next_cursor" is null on the last page.
curl -s "$BASE/v1/projects/$PID/tasks?limit=2" -H "$AUTH"
export CURSOR=<paste next_cursor from the response above>
curl -s "$BASE/v1/projects/$PID/tasks?limit=2&cursor=$CURSOR" -H "$AUTH"

# 6. Patch: only the fields you send change. Send "due_date":"" to clear the date.
export TID=<paste a task "id" here>
curl -s -X PATCH $BASE/v1/tasks/$TID -H "$AUTH" -H "$JSON" -d '{"status":"done"}'

# 7. Filter by status.
curl -s "$BASE/v1/projects/$PID/tasks?status=done" -H "$AUTH"

# 8. Delete a task, then the whole project (its remaining tasks go too).
curl -s -i -X DELETE $BASE/v1/tasks/$TID -H "$AUTH"        # 204 No Content
curl -s -i -X DELETE $BASE/v1/projects/$PID -H "$AUTH"     # 204
```

Things that should fail (each is covered by a test):

```sh
curl -s -i $BASE/v1/me                                        # 401: no token (WWW-Authenticate header)
curl -s -X POST $BASE/v1/projects/$PID/tasks -H "$AUTH" -H "$JSON" \
  -d '{"title":"","priority":9}'                              # 422 with per-field messages
curl -s -X POST $BASE/v1/projects -H "$AUTH" -d '{"name":'    # 400 malformed JSON

# Sign up a second user "bob", log in as him, and try Ada's data:
curl -s -i $BASE/v1/projects/$PID -H "Authorization: Bearer $BOB_TOKEN"   # 404, NOT 403
curl -s $BASE/metrics | grep http_requests_total              # note route="/v1/projects/{id}", not the raw id
```

## Design decisions and tradeoffs

**Standard library routing, no framework.** Since Go 1.22 `ServeMux` handles methods, path
parameters and automatic 405s. A router dependency would add little. Tradeoff: unmatched paths get
the mux's plain-text 404/405 instead of our JSON envelope.

**Handlers return `error`.** `func(w, r) error` wrapped by one adapter means no handler writes its own
error response; `mapError` (respond.go) is the only place that decides statuses. Errors are wrapped with
`%w` at each layer, so `errors.Is(err, domain.ErrNotFound)` still works at the top, and the client message
is a fixed string, never `err.Error()` (which could contain SQL or paths).

**404, not 403, for other users' resources.** A 403 confirms the id exists. Ownership is checked *inside
the SQL* (`WHERE id = ? AND owner_id = ?`, tasks via a subquery on their project), so there is no
check-then-act race and a missing row and someone else's row are literally the same result.

**sqlc instead of an ORM.** You write real SQL, sqlc generates typed Go and checks the SQL against the
schema at generate time. The goose migrations are sqlc's schema, so they cannot drift. Generated code
is committed so a plain `go build` works without any tools.

**UUIDv7 ids + keyset pagination.** v7 ids are time-ordered, so `WHERE id > :cursor ORDER BY id LIMIT n`
is an index range scan that stays fast on page 10,000 and is stable while rows are inserted (OFFSET is
neither). The service fetches `limit+1` rows to know whether another page exists without a `COUNT`.
Cursors are opaque base64 of the last id and carry no authority; the query still filters by owner.
Tradeoff: order is creation time at millisecond resolution, not a user-chosen sort.

**Atomic PATCH.** `UpdateTask` reads, applies the patch and writes inside one `BEGIN IMMEDIATE`
transaction, so two concurrent PATCHes cannot overwrite each other's fields (tested with 20 goroutines).
Tradeoff: SQLite has a single writer; PostgreSQL would use `SELECT ... FOR UPDATE` or optimistic
versioning.

**Stateless JWTs.** No session table, any instance can verify a token. Tradeoff: a token cannot be
revoked before it expires (a deleted user's token still verifies, `/v1/me` then 404s). Mitigation: a
short TTL plus refresh tokens (next steps). Parsing pins `HS256`, requires `exp` and the issuer, and
the secret must be >= 32 bytes; this closes the classic `alg: none` and algorithm-confusion attacks.

**Login does not reveal whether an email exists.** Same 401 body, and an unknown email still pays for a
bcrypt comparison against a dummy hash so response *time* does not leak it either. (Signup does return
409 for a taken email; most products accept that for usability.)

**Rate limiting by `RemoteAddr`.** `X-Forwarded-For` is client-controlled; trusting it lets an attacker
mint a fresh bucket per request. Tradeoff: behind a reverse proxy all users look like one IP until you
add a trusted-proxy allow-list. Limits are per process, so N replicas allow N times the rate (use Redis
or the API gateway for a shared limit). The limiter's janitor goroutine stops with its context (tested).

**Metrics use the route pattern.** `r.Pattern` (`/v1/tasks/{id}`) rather than the URL path keeps the
label set bounded; unknown HTTP methods collapse to `OTHER`. Requests rejected before routing (429) and
unknown paths are bucketed as `route="unmatched"`. Metrics are registered on a per-handler registry,
not the global default, so there is no global state.

**No global state, dependencies passed explicitly.** `run(ctx, args, getenv, stdout)` takes everything
it touches, so the whole app is tested in-process (`cmd/api/main_test.go`), including graceful shutdown.
The clock and id generator are plain function fields that tests replace.

**SQLite specifics.** Pragmas live in the DSN so *every* pooled connection gets them: `foreign_keys`,
WAL, `busy_timeout`, `synchronous(NORMAL)`, and `_txlock=immediate`. Tests use a temp *file*, never
`:memory:` (each pooled connection would see its own empty database).

**Security checklist** (all in the code, with comments saying why): parameterized SQL only (sqlc);
bcrypt cost 12 with a 72-byte input cap; JWT algorithm pinned; secret required and redacted
(`slog.LogValuer`), never logged along with passwords, tokens or Authorization headers; `MaxBytesReader`
1 MiB; unknown JSON fields rejected; server read/write/idle timeouts; non-root, shell-less container;
panic recovery without leaking details.

## Layout

```
cmd/api/             main.go (wiring, graceful shutdown) + main_test.go (starts the whole app in-process)
internal/config/     env -> Config, secret redaction
internal/domain/     entities, validation, sentinel errors
internal/auth/       password hashing, JWT issuer/verifier
internal/service/    business logic + repository interfaces (tests use in-memory fakes)
internal/store/      migrations/, queries/, sqlcdb/ (generated), store.go (maps rows and errors)
internal/httpapi/    server.go (routes + middleware chain), middleware.go, ratelimit.go, respond.go,
                     pagination.go, handlers_*.go
api/openapi.yaml     API contract
sqlc.yaml            code generation config
Dockerfile           multi-stage, distroless, non-root
```

## Next steps

1. **PostgreSQL.** The store is the only layer that knows about SQLite. Follow
   [`docs/postgres-later.md`](../../docs/postgres-later.md): switch the sqlc engine to `postgresql`, the
   goose dialect to `postgres`, the driver to `pgx`, swap the error-code mapping (`23505` unique,
   `23503` foreign key), and run the same HTTP tests against a Postgres container.
2. **Refresh tokens.** Short-lived access tokens (5-15 minutes) plus a rotating, revocable refresh token
   stored hashed in the database; add logout and "revoke all sessions".
3. **OpenTelemetry.** Wrap the handler with `otelhttp`, trace the database calls, export to a collector;
   put the trace id in the access log next to `request_id`.
4. **Trusted proxies** for the rate limiter, per-user (not just per-IP) limits, and login lockout /
   throttling per account.
5. **CI/CD:** build the image in CI, run `govulncheck`, run migrations as a separate deploy job.
6. **Features:** task assignment and comments, project sharing between users (this is where `403`
   finally becomes meaningful), ETags / `If-Match` for optimistic concurrency on PATCH.
