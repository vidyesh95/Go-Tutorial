# taskapi - capstone REST API

A production-style task-tracking API in Go: users own **projects**, projects contain **tasks**.
Standard-library routing, SQLite with type-safe SQL (sqlc) and embedded migrations (goose), **two
authentication modes in one app** (stateless Bearer JWT and Better Auth-style database sessions in a
cookie, with email verification, password reset and GitHub sign-in), Argon2id password hashing,
structured logging, Prometheus metrics, per-IP rate limiting, graceful shutdown, a distroless Docker
image, and tests at every layer.

It deliberately uses very few dependencies, so you can explain every line of it in an interview.

| Concern | Choice |
|---|---|
| HTTP | `net/http` + Go 1.22 `ServeMux` patterns (`GET /v1/tasks/{id}`) |
| Database | SQLite (pure Go driver `modernc.org/sqlite`, no cgo), WAL mode |
| SQL | [sqlc](https://sqlc.dev) generates Go from `queries/*.sql`; no ORM |
| Migrations | [goose](https://github.com/pressly/goose) Provider API, files embedded in the binary |
| Passwords | **Argon2id** (`x/crypto/argon2`, OWASP parameters), PHC strings, rehash-on-login |
| Auth mode 1 | HS256 JWT bearer tokens (`golang-jwt/jwt/v5`): stateless, for mobile/API/service clients |
| Auth mode 2 | DB-backed sessions in an HttpOnly cookie, CSRF-protected (`http.CrossOriginProtection`), email verification, password reset, GitHub OAuth + PKCE (`x/oauth2`): for web clients |
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
# TASKAPI_COOKIE_SECURE=false only because this local demo is plain http; keep the default (true) behind HTTPS
docker run --rm -p 8080:8080 -e TASKAPI_JWT_SECRET="$(openssl rand -base64 48)" \
  -e TASKAPI_COOKIE_SECURE=false \
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
| `TASKAPI_SESSION_TTL` | `168h` | Cookie-session lifetime after its last refresh (7 days; `time.ParseDuration` has no `d`) |
| `TASKAPI_SESSION_UPDATE_AGE` | `24h` | Sliding expiry: an active session is extended at most this often (Better Auth's `updateAge`) |
| `TASKAPI_COOKIE_SECURE` | `true` | `Secure` flag on cookies. **Set `false` only for local plain-http development** (curl and Safari do not send Secure cookies over http) |
| `TASKAPI_PUBLIC_URL` | `http://localhost:8080` | Public base URL: links in emails and the GitHub callback URL (`https://...` in production) |
| `TASKAPI_AUTH_RATE_RPS` / `_BURST` | `0.5` / `10` | Stricter per-IP limit on sign-in, sign-up, reset and verification endpoints (JWT login too) |
| `TASKAPI_GITHUB_CLIENT_ID` / `_SECRET` | unset | Enable "Sign in with GitHub" (both or neither; secret is redacted in logs). Routes are 404 when unset |

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
   |   AccountRepo, SessionRepo, VerificationRepo, ProjectRepo,    |
   |   TaskRepo, Mailer  (consumer-defined)                        |
   +-------------------------------------------------------------+
        |  uses                                  ^  implemented by
        v                                        |
   internal/auth                      +--------------------------------+
   argon2id, JWT, opaque tokens,      |  internal/store                |   SQL + error translation
   GitHub OAuth client                |   (internal/mailer: Console / in-memory test double)
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
  |  POST /v1/projects/{id}/tasks   Authorization: Bearer <jwt>    (or: Cookie: taskapi_session=<token>)
  v
requestID        assign/accept X-Request-ID, put it in the context
accessLog        (after the response) one slog line: method, route, status, duration, request_id
metrics          (after the response) http_requests_total / http_request_duration_seconds
recoverer        panic -> logged stack trace + clean 500
cors             answer browser preflights, set Access-Control-* for allowed origins
rateLimit        per-IP token bucket; 429 + Retry-After when empty
ServeMux         match "POST /v1/projects/{id}/tasks"; sets r.Pattern; 404/405 for the rest
requireAuth      Bearer JWT (signature, HS256 only, issuer, expiry) OR session cookie (CSRF check, DB lookup,
                 sliding expiry) -> user id + auth method in context
handler          decodeJSON (1 MiB cap, unknown fields rejected) -> service call -> writeJSON
service          validate input; prove the caller owns the project; build the Task (uuid v7, UTC time)
store            INSERT via sqlc (parameterized); map driver errors to domain errors
  |
  v  any error travels back up, wrapped with context at each layer ("create task: store: create task: ...")
mapError         the ONE place that turns domain errors into status codes + the JSON error envelope
```

### Data model

```
users(id, email UNIQUE, name, email_verified, image, created_at, updated_at)
  1 --- * account(id, user_id, provider_id 'credential'|'github', account_id, password (argon2id, credential only),
            |        OAuth tokens/scope, UNIQUE(provider_id, account_id))
  1 --- * session(id, user_id, token_hash UNIQUE (SHA-256), expires_at, ip_address, user_agent, ...)
  (verification(id, identifier 'email-verification:<uid>'|'password-reset:<uid>', value = SHA-256 of token, expires_at))
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
| `POST /v1/auth/signup` | no | `{email, name, password}` -> 201 user (JWT mode) |
| `POST /v1/auth/login` | no | `{email, password}` -> `{token, expires_at}` (JWT mode) |
| `GET /v1/me` | yes (either mode) | current user |
| `POST /v1/session-auth/sign-up/email` | no | `{email, name, password}` -> 201 `{user, session}` + cookie + verification email |
| `POST /v1/session-auth/sign-in/email` | no | `{email, password}` -> `{user, session}` + cookie |
| `POST /v1/session-auth/sign-out` | cookie | delete the session, clear the cookie (idempotent) |
| `GET /v1/session-auth/get-session` | cookie | `{user, session}` |
| `GET /v1/session-auth/list-sessions` | cookie | my active sessions, `current: true` on this one |
| `POST /v1/session-auth/revoke-session` | cookie | `{id}`; only my own sessions (404 otherwise) |
| `POST /v1/session-auth/revoke-other-sessions` | cookie | sign out every other device |
| `POST /v1/session-auth/send-verification-email` | either | resend the link |
| `GET /v1/session-auth/verify-email?token=` | no | single-use, 24 h |
| `POST /v1/session-auth/forget-password` | no | `{email}`; always 200 (no enumeration) |
| `POST /v1/session-auth/reset-password` | no | `{token, new_password}`; single-use, 1 h; revokes ALL sessions |
| `GET /v1/session-auth/oauth/github` + `/callback` | no | GitHub sign-in (state + PKCE); 404 unless configured |
| `GET /v1/projects` | yes | my projects (cursor pagination) |
| `POST /v1/projects` | yes | `{name}` -> 201 (409 if the name is taken) |
| `GET/PATCH/DELETE /v1/projects/{id}` | yes | read, rename, delete (cascades to tasks) |
| `GET /v1/projects/{id}/tasks` | yes | `?status=todo\|doing\|done&cursor=&limit=` |
| `POST /v1/projects/{id}/tasks` | yes | `{title, priority?, due_date?}` -> 201 + `Location` |
| `GET/PATCH/DELETE /v1/tasks/{id}` | yes | read, partial update, delete |
| `GET /healthz` `/readyz` `/metrics` | no | liveness, readiness (503 while draining), Prometheus |

Errors always look like
`{"error": {"code": "validation_failed", "message": "...", "fields": {"title": "..."}, "request_id": "..."}}`.
Statuses: 400 malformed JSON or an invalid/expired emailed token, 401 bad/missing credentials, 403 rejected
cross-origin (CSRF) request, 404 not found *or not yours*, 409 duplicate, 413 body over 1 MiB, 422 invalid values,
429 rate limited, 502 GitHub failed, 500 server fault (details only in the log).

## Authentication: two modes in one app

The same user table and the same protected routes (`/v1/projects`, `/v1/tasks`, `/v1/me`) accept **either**
credential. Pick per client, not per deployment.

```
                     ┌────────────────────── Mode 1: stateless JWT ───────────────────────┐
 mobile app /        │ POST /v1/auth/login {email,password}  ->  {"token": "<jwt>"}        │
 service / script ──►│ every request:  Authorization: Bearer <jwt>                         │──┐
                     │ server: verify signature + expiry, NO database lookup               │  │
                     └─────────────────────────────────────────────────────────────────────┘  │   requireAuth
                                                                                              ├──► user id in ctx ──► handler
                     ┌────────────── Mode 2: database session in a cookie ────────────────┐  │
 browser app ───────►│ POST /v1/session-auth/sign-in/email -> Set-Cookie: taskapi_session  │  │
                     │ every request:  Cookie: taskapi_session=<random token>              │──┘
                     │ server: CSRF check, SHA-256(token) -> session row (expiry, slide)   │
                     └─────────────────────────────────────────────────────────────────────┘
        both modes check the password the same way: account(provider 'credential').password (Argon2id)
```

If an `Authorization` header is present it alone decides (a bad Bearer token is a 401, never a silent
fall-through to the cookie).

### JWT vs database sessions

| | Bearer JWT (`/v1/auth/*`) | DB session cookie (`/v1/session-auth/*`) |
|---|---|---|
| State on the server | none | a `session` row per login |
| Per-request cost | HMAC check | one indexed lookup (and an occasional write for sliding expiry) |
| **Revocation** | impossible before `exp` (a deleted user's token still verifies) | instant: delete the row; "sign out other devices", "sign out everywhere after a password reset" |
| Scaling | any instance verifies, no shared store | all instances share the database (or a cache in front of it) |
| Where the secret lives | client code (memory/keychain); you must protect it | **HttpOnly** cookie: JavaScript cannot read it, so XSS cannot exfiltrate it |
| CSRF | not applicable: the browser never attaches an `Authorization` header on its own | **applicable**: the browser attaches cookies automatically, so `http.CrossOriginProtection` + `SameSite=Lax` guard every cookie request |
| Best for | mobile apps, service-to-service, scripts, third-party API clients | browser apps, anything that needs "log out", device lists, or admin revocation |
| Storage if the DB leaks | n/a (nothing stored) | only `SHA-256(token)` is stored, so the leak does not hand out live sessions |

### Better Auth schema mapping

The cookie mode follows [Better Auth](https://www.better-auth.com/docs/concepts/database)'s data model: *who* a person
is (`user`) is separate from *how* they prove it (`account`), from the logins in progress (`session`) and from the
one-time emailed links (`verification`). Migration `00002_better_auth.sql` adapts it to SQLite and snake_case.

| Better Auth (camelCase) | Here (snake_case) | Notes / deviation |
|---|---|---|
| `user.emailVerified` | `users.email_verified` | `INTEGER 0/1` (SQLite has no boolean) |
| `user.image`, `user.updatedAt` | `users.image`, `users.updated_at` | `updated_at` backfilled from `created_at` |
| `user` table | `users` | kept our existing plural table; `password_hash` is **dropped** (moved to `account`) |
| `account.providerId` | `account.provider_id` | `'credential'` or `'github'` |
| `account.accountId` | `account.account_id` | credential: the user's own id; github: GitHub's numeric id |
| `account.password` | `account.password` | **Argon2id PHC string** (Better Auth defaults to scrypt) |
| `account.accessToken`, `refreshToken`, `idToken` | `access_token`, `refresh_token`, `id_token` | stored as Better Auth does; **encrypt at rest** before you use them for API calls |
| `account.accessTokenExpiresAt`, `refreshTokenExpiresAt`, `scope` | `access_token_expires_at`, `refresh_token_expires_at`, `scope` | |
| `session.token` | `session.token_hash` | **deviation: SHA-256 of the cookie value, not the value.** A database leak does not leak live sessions |
| `session.expiresAt`, `ipAddress`, `userAgent`, `createdAt`, `updatedAt` | `expires_at`, `ip_address`, `user_agent`, `created_at`, `updated_at` | `updated_at` also drives sliding expiry |
| `verification.identifier` | `verification.identifier` | `email-verification:<user id>` / `password-reset:<user id>` |
| `verification.value` | `verification.value` | **deviation: SHA-256 of the emailed token**; `UNIQUE` because the link carries only the token and we look it up by value |
| `verification.expiresAt` | `verification.expires_at` | 24 h (verify email), 1 h (reset) |

The migration is **expand/contract**: it adds the new tables and columns, copies every `users.password_hash` into a
`credential` account (account id = user id), then drops the old column. `Down` restores it. It never edits
`00001`, and `internal/store/migration_test.go` upgrades a version-1 database holding real rows, asserts the data
moved, then runs Down and Up again.

### Argon2id (`internal/auth/password.go`)

- **Why not bcrypt?** An attacker with a stolen database guesses offline, on GPUs/ASICs with thousands of cores.
  bcrypt needs only ~4 KiB of RAM per guess, so those cores run it almost for free. Argon2id is *memory-hard*: each guess
  must fill and re-read tens of MiB, and RAM per core is exactly what GPUs and ASICs lack. Argon2id (the hybrid variant)
  also resists side-channel and time-memory-tradeoff attacks. It has no 72-byte input limit either.
- **Parameters** (`auth.DefaultParams`): the OWASP Password Storage Cheat Sheet's first choice, **m = 19 MiB, t = 2,
  p = 1**, 16-byte random salt, 32-byte key. OWASP lists equivalent trade-offs (e.g. m = 46 MiB, t = 1). RFC 9106's
  memory-constrained recommendation is m = 64 MiB, t = 3, p = 4; use it if you have the RAM. Aim for 50-250 ms per hash on
  *your* hardware and re-measure every few years.
- **PHC string**: `$argon2id$v=19$m=19456,t=2,p=1$<base64 salt>$<base64 hash>` (standard base64, no padding). The string
  carries its own algorithm, version, parameters and salt, so there is no salt column and old hashes stay verifiable
  after you raise the parameters. `Verify` parses it strictly: only `argon2id`, only version 19, and **parameter
  bounds** (memory <= 256 MiB, t <= 10, ...) so a poisoned database row cannot make one login allocate gigabytes.
  The comparison uses `crypto/subtle.ConstantTimeCompare`.
- **Rehash on login**: `Verify` returns `(ok, needsRehash, err)`. If the password matched but the stored parameters
  differ from the current ones, login computes a fresh hash and updates the account row. Login is the only moment the
  plaintext exists, so this upgrades active accounts silently when you raise the cost.
- **Input limits**: 8-128 bytes. 128 bounds CPU/memory per request; Argon2 itself has no limit, unlike bcrypt's 72.
  A semaphore (`GOMAXPROCS` slots) bounds *concurrent* hashes: each holds ~19 MiB, and 200 simultaneous logins would
  otherwise allocate ~4 GB.
- **Timing**: an unknown email still verifies against a dummy Argon2id hash (made with the current parameters), so
  "no such user" costs the same as "wrong password".

#### Migrating legacy hashes (bcrypt -> Argon2id)

Argon2id is *required* here, so `Verify` rejects anything else (`ErrUnsupportedAlgorithm`) and login answers 401. Dev
databases created before this change contain bcrypt hashes; those users must use **forget password** once (which sets an
Argon2id hash). The common alternative for a real migration: on login, if the stored hash starts with `$2`, verify it
with bcrypt, and on success immediately store an Argon2id hash (the same "rehash on login" pattern). It migrates
silently but keeps bcrypt in your dependency tree until every dormant account has either logged in or been expired.
We chose the explicit reset to keep one algorithm in the code.

### Cookie-session details

- **Token**: 32 random bytes (`crypto/rand`), base64url. The database stores `hex(SHA-256(token))`. SHA-256 (not
  Argon2) is right *here*: a 256-bit random value cannot be guessed, so a fast hash is enough and the per-request lookup
  stays cheap.
- **Cookie** `taskapi_session`: `HttpOnly` (XSS cannot read it), `Secure` (HTTPS only; `TASKAPI_COOKIE_SECURE=false`
  for local http), `SameSite=Lax` (not sent on cross-site subrequests/POSTs, but sent when following a link), `Path=/`,
  no `Domain`, `Max-Age` = TTL. Hardening step: rename it `__Host-taskapi_session` so browsers enforce those attributes.
- **Expiry**: 7 days. **Sliding** like Better Auth's `updateAge`: when a request arrives and the session was last
  updated >= 24 h ago, `expires_at` moves to now + 7 days and the cookie is re-sent. Active users never get logged out;
  there is no database write on every request. Expired rows are deleted when presented (lazy) and by a cleanup goroutine
  (hourly, stopped by the context) together with expired verification rows.
- **CSRF**: browsers attach cookies to *any* request to our origin, including ones triggered by another site's form.
  Every cookie-authenticated request, and every state-changing credential endpoint (sign-in, sign-up, sign-out,
  password reset: "login CSRF" is real), goes through Go 1.25's `http.CrossOriginProtection`. It rejects unsafe
  methods whose `Sec-Fetch-Site` is not `same-origin`/`none`, or whose `Origin` host differs from `Host`; requests with
  neither header (curl, mobile apps) pass. Origins in `TASKAPI_CORS_ORIGINS` are trusted, and *only* those origins get
  `Access-Control-Allow-Credentials` (never with `*`). `SameSite=Lax` is the second, independent layer. Bearer requests
  skip the check: a malicious page cannot make the browser add an `Authorization` header.
- **Email verification / reset**: a token is 32 random bytes; only its SHA-256 is stored. Single use is one atomic
  `DELETE ... RETURNING` (two concurrent requests cannot both succeed; tested under race). A reset validates the new
  password *before* consuming the token, replaces the Argon2id hash and deletes **all** of the user's sessions in one
  transaction. `forget-password` answers 200 for unknown emails (no enumeration). Emails go through a `Mailer`
  interface; the default `Console` mailer **logs the message including the link**: dev only, swap in SES/Postmark/SMTP
  (ideally behind a queue, which also removes the response-time side channel between known and unknown emails).
- **GitHub** (`TASKAPI_GITHUB_CLIENT_ID/SECRET`): authorization-code flow with `state` and **PKCE (S256)**. `state` and the
  verifier live in a 10-minute HttpOnly cookie scoped to the OAuth paths. The callback compares state in constant time,
  exchanges the code with the verifier, reads `/user` and `/user/emails`, and requires a **verified primary email**.
  Then: known GitHub id -> sign in; local user with that email -> link *only if their email is verified* (an unverified
  local account could have been pre-registered by an attacker, so it gets 409 instead of being merged: "account
  pre-hijacking"); otherwise create a verified user with a `github` account. A GitHub-only user can add a password through
  forget-password.
- **Rate limits**: besides the global per-IP bucket, sign-in/sign-up/reset/verification (and JWT login/signup) share a
  stricter one (`TASKAPI_AUTH_RATE_*`, default 30/min, burst 10). It limits per IP only; see next steps for per-account
  lockout.

### Cookie-session walkthrough

Run the server with a non-Secure cookie (plain-http local development only) and read emails from its log:

```sh
export TASKAPI_JWT_SECRET="$(openssl rand -base64 48)"
TASKAPI_COOKIE_SECURE=false TASKAPI_LOG_FORMAT=text go run ./05-projects/taskapi/cmd/api   # terminal 1
```

```sh
BASE=http://localhost:8080
JSON='Content-Type: application/json'
cd "$(mktemp -d)"                                # cookie jars go here
J="-c cookies.txt -b cookies.txt"                # curl: store AND send cookies

# 1. Sign up: 201, and the cookie jar now holds taskapi_session (HttpOnly; the body has no token).
curl -s -i $J -X POST $BASE/v1/session-auth/sign-up/email -H "$JSON" \
  -d '{"email":"ada@example.com","name":"Ada","password":"correct horse battery"}'

# 2. Who am I? The REST API works with the cookie alone.
curl -s $J $BASE/v1/session-auth/get-session
curl -s $J -X POST $BASE/v1/projects -H "$JSON" -d '{"name":"Website"}'

# 3. A second device signs in with its own cookie jar; list-sessions shows both (current: true on yours).
curl -s -c phone.txt -b phone.txt -X POST $BASE/v1/session-auth/sign-in/email -H "$JSON" \
  -d '{"email":"ada@example.com","password":"correct horse battery"}' >/dev/null
curl -s $J $BASE/v1/session-auth/list-sessions
curl -s $J -X POST $BASE/v1/session-auth/revoke-other-sessions
curl -s -o /dev/null -w "phone is now: %{http_code}\n" -b phone.txt $BASE/v1/me     # 401: revoked server-side

# 4. Verify the email: the console mailer printed the link in the server log (terminal 1).
curl -s "$BASE/v1/session-auth/verify-email?token=<token from the log line>"
curl -s $J $BASE/v1/me                                                          # "email_verified": true

# 5. CSRF: what a malicious page's forged POST looks like to the server. Browsers add Sec-Fetch-Site.
curl -s -o /dev/null -w "cross-site POST: %{http_code}\n" $J -X POST $BASE/v1/projects \
  -H "$JSON" -H 'Sec-Fetch-Site: cross-site' -d '{"name":"evil"}'                 # 403

# 6. The JWT mode keeps working on the same account and the same routes.
TOKEN=$(curl -s -X POST $BASE/v1/auth/login -H "$JSON" \
  -d '{"email":"ada@example.com","password":"correct horse battery"}' | jq -r .token)
curl -s $BASE/v1/projects -H "Authorization: Bearer $TOKEN"

# 7. Forgot password: read the reset token from the log, then set a new password.
curl -s -X POST $BASE/v1/session-auth/forget-password -H "$JSON" -d '{"email":"ada@example.com"}'
curl -s -X POST $BASE/v1/session-auth/reset-password -H "$JSON" \
  -d '{"token":"<token from the log line>","new_password":"a new secret phrase"}'
curl -s -o /dev/null -w "old cookie after reset: %{http_code}\n" -b cookies.txt $BASE/v1/me   # 401: all sessions deleted

# 8. Sign in again, then sign out (cookie cleared; the session row is deleted).
curl -s $J -X POST $BASE/v1/session-auth/sign-in/email -H "$JSON" -d '{"email":"ada@example.com","password":"a new secret phrase"}' >/dev/null
curl -s $J -X POST $BASE/v1/session-auth/sign-out
curl -s -o /dev/null -w "after sign-out: %{http_code}\n" -b cookies.txt $BASE/v1/me     # 401
```

For GitHub: create an OAuth App with callback `http://localhost:8080/v1/session-auth/oauth/github/callback`, export
`TASKAPI_GITHUB_CLIENT_ID` / `TASKAPI_GITHUB_CLIENT_SECRET`, restart, and open
`http://localhost:8080/v1/session-auth/oauth/github` in a browser (the flow needs a real browser; the tests drive it
against an `httptest` fake of GitHub that also verifies PKCE).

## curl walkthrough (JWT mode)

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

**Two authentication modes, one set of routes.** A JWT is stateless and cheap but cannot be revoked; a database
session costs a lookup but can be revoked, listed and expired. Rather than pick one, `requireAuth` accepts either and
the handlers only see a user id (and which method was used). Both share `AuthService.Authenticate`, so there is one
definition of a valid password. The tradeoffs are in the table under **Authentication**.

**Stateless JWTs.** No session table, any instance can verify a token. Tradeoff: a token cannot be
revoked before it expires (a deleted user's token still verifies, `/v1/me` then 404s; a password reset does not
log JWT clients out). Mitigation: a short TTL plus refresh tokens (next steps), or use cookie sessions where
revocation matters. Parsing pins `HS256`, requires `exp` and the issuer, and the secret must be >= 32 bytes; this
closes the classic `alg: none` and algorithm-confusion attacks.

**Login does not reveal whether an email exists.** Same 401 body for both login modes, and an unknown email (or a
GitHub-only account, or a legacy hash) still pays for an Argon2id verification against a dummy hash so response *time*
does not leak it either. (Signup does return 409 for a taken email; most products accept that for usability.)

**CORS and cookies.** Credentialed CORS (`Access-Control-Allow-Credentials: true`) is sent only for origins listed
explicitly in `TASKAPI_CORS_ORIGINS`, never for `*`, and the same origins are trusted by the CSRF check. A SPA on another
*site* (not just another port or subdomain) will not receive a `SameSite=Lax` cookie on `fetch`; host the API and app on
the same site, or use Bearer tokens for that client.

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
Argon2id (OWASP parameters, bounded parsing, constant-time compare, bounded concurrency) with a 128-byte input cap;
session tokens and emailed tokens stored only as SHA-256; HttpOnly + SameSite cookies and `CrossOriginProtection` for
cookie auth; PKCE + `state` for OAuth; JWT algorithm pinned; secret required and redacted
(`slog.LogValuer`), never logged along with passwords, tokens or Authorization headers; `MaxBytesReader`
1 MiB; unknown JSON fields rejected; server read/write/idle timeouts; non-root, shell-less container;
panic recovery without leaking details.

## Layout

```
cmd/api/             main.go (wiring, graceful shutdown) + main_test.go (starts the whole app in-process)
internal/config/     env -> Config, secret redaction
internal/domain/     entities, validation, sentinel errors
internal/auth/       Argon2id hasher, JWT issuer/verifier, opaque tokens (sessions, emails), GitHub OAuth client
                     (githubtest/: an httptest fake of GitHub that verifies PKCE)
internal/mailer/     Mailer implementations: Console (logs the link) and Memory (test double)
internal/service/    business logic + repository interfaces (tests use in-memory fakes)
internal/store/      migrations/, queries/, sqlcdb/ (generated), store.go (maps rows and errors)
internal/httpapi/    server.go (routes + middleware chain), middleware.go (requireAuth: JWT or cookie), cookies.go,
                     ratelimit.go, respond.go, pagination.go, handlers_*.go (handlers_session.go, handlers_oauth.go)
api/openapi.yaml     API contract
sqlc.yaml            code generation config
Dockerfile           multi-stage, distroless, non-root
```

## Next steps

1. **PostgreSQL.** The store is the only layer that knows about SQLite. Follow
   [`docs/postgres-later.md`](../../docs/postgres-later.md): switch the sqlc engine to `postgresql`, the
   goose dialect to `postgres`, the driver to `pgx`, swap the error-code mapping (`23505` unique,
   `23503` foreign key), and run the same HTTP tests against a Postgres container.
2. **Refresh tokens for the JWT mode.** Short-lived access tokens (5-15 minutes) plus a rotating, revocable refresh
   token stored hashed in the database (the cookie mode already has logout and "revoke all sessions").
   Also: **require verified email** before allowing writes, **encrypt provider tokens** (AES-GCM) if you start using them,
   send mail through a real provider behind a queue, add TOTP/passkeys, and a "password changed" notification email.
3. **OpenTelemetry.** Wrap the handler with `otelhttp`, trace the database calls, export to a collector;
   put the trace id in the access log next to `request_id`.
4. **Trusted proxies** for the rate limiter, per-user (not just per-IP) limits, and login lockout /
   throttling per *account* (the strict auth limit is per IP and can be spread across many IPs).
5. **CI/CD:** build the image in CI, run `govulncheck`, run migrations as a separate deploy job.
6. **Features:** task assignment and comments, project sharing between users (this is where `403`
   finally becomes meaningful), ETags / `If-Match` for optimistic concurrency on PATCH.
