# What Go teams actually use

Go's culture favors **small dependencies and the standard library**. Most production Go backends
look surprisingly similar. This page summarizes the common choices so you know what to learn first.

## The typical Go backend stack

| Layer | Most common choice | Also seen | Notes |
|-------|--------------------|-----------|-------|
| HTTP routing | `net/http` (Go 1.22+ `ServeMux` patterns), **chi** | Gin, Echo, Fiber | chi = stdlib-compatible handlers, so it's the "safe" pick |
| JSON | `encoding/json` | `encoding/json/v2` (importable since Go 1.27), sonic | Stdlib is fine for nearly everyone |
| DB driver | **pgx** (PostgreSQL) | `database/sql` + driver | pgx is the de-facto Postgres driver |
| Query layer | **sqlc** (generate code from SQL), **sqlx**, raw `database/sql` | GORM, ent, bun | Most job postings mention sqlc/sqlx or "raw SQL" |
| Migrations | **goose**, golang-migrate, atlas | — | Plain `.sql` files, versioned in git |
| Logging | `log/slog` (stdlib since 1.21) | zap, zerolog | New code uses slog |
| Config | env vars + small struct | viper, envconfig | 12-factor style |
| Auth | JWT (`golang-jwt/jwt/v5`), sessions, OAuth2 (`x/oauth2`) | — | Argon2id (OWASP first choice) or bcrypt for passwords; DB sessions for web, JWT for APIs |
| Validation | hand-written or `go-playground/validator` | — | Gin bundles validator |
| RPC between services | **gRPC + protobuf** (buf) | Connect, Twirp, plain HTTP/JSON | gRPC is standard at bigger companies |
| Messaging | Kafka, NATS, RabbitMQ, SQS | Redis streams | async work and events |
| Observability | OpenTelemetry, Prometheus | Datadog, Grafana | traces + metrics + logs |
| Testing | `testing` + `httptest` + table tests | testify, testcontainers-go | testify's `require` is very common |
| Lint | golangci-lint | — | Runs in CI almost everywhere |
| Deploy | Docker (distroless/scratch), Kubernetes | Cloud Run, ECS, Lambda | Go builds static binaries → tiny images |

## Why "no ORM"?

- Go has no inheritance or annotations, so ORMs feel awkward and hide what SQL runs.
- Performance and query plans matter at scale; you want to see and tune the SQL.
- **sqlc** gives ORM-like type safety *without* an ORM: you write SQL, it generates Go.
- **sqlx** is a thin layer over `database/sql` that scans rows into structs.
- GORM is still used (especially at startups for CRUD-heavy apps) — worth recognizing, not required.

## Frameworks: when teams pick what

- **Standard library / chi**: default for new services, libraries and companies that value
  long-term maintenance. Handlers are plain `http.Handler`, so all middleware is interchangeable.
- **Gin**: very popular, lots of tutorials, used by many startups. Own `*gin.Context`, but built on
  `net/http`.
- **Echo**: similar to Gin.
- **Fiber**: Express-like API built on **fasthttp**, not `net/http`. Fast in benchmarks but not
  compatible with the stdlib middleware ecosystem. More common with developers coming from Node.

Learn stdlib first: once you understand `http.Handler`, every framework is a thin wrapper.

## Where Go is used

- **Backend APIs & microservices** — Uber, Monzo, Stripe (parts), Twitch, PayPal, many fintechs
- **Cloud / infrastructure** — Docker, Kubernetes, Terraform, Prometheus, etcd, Consul
- **CLI tools** — `gh`, `kubectl`, `hugo`, `terraform`
- **Networking** — proxies (Caddy, Traefik), DNS, load balancers
- **Data pipelines / workers** — queue consumers, ETL, schedulers

## What interviews usually cover

1. Language: slices vs arrays, maps, interfaces, pointers vs values, errors, generics.
2. Concurrency: goroutines, channels, select, mutex vs channel, context cancellation, leaks, races.
3. HTTP: handler design, middleware, timeouts, graceful shutdown, status codes, idempotency.
4. SQL: joins, indexes, transactions & isolation, N+1 queries, pagination, migrations.
5. System design: caching, queues, rate limiting, retries/backoff, service boundaries.
6. Testing: table-driven tests, mocking via small interfaces, integration tests.

See [interview-prep.md](interview-prep.md) for questions with answers.
