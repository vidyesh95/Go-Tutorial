# Go-Tutorial <img src="https://github.com/vidyesh95/Go-Tutorial/assets/13806493/ab0f1d12-9f5b-4c1e-ab05-f6cde0d372db" alt="drawing" width="24"/>

A hands-on path from **zero Go** to **job-ready Go backend engineer**: language, concurrency, SQL,
REST APIs, frameworks, microservices and the other things Go is used for in industry.

Built for **Go 1.27.1+**. Every lesson is a runnable program with notes in its comments.

## Quick start

```shell
go version                               # needs go1.27.1 or newer
go run ./01-fundamentals/01hello         # run one lesson
make help                                # list all helper tasks
make test                                # run every test in the repo
```

`go run ./path/to/lesson` compiles and runs the `main` package in that folder.
`go build -o bin/app ./path` produces a binary instead (`bin/` is git-ignored).

## Roadmap

Do the tracks in order. Each folder has a `README.md` with the lesson list and interview questions.

| # | Track | What you learn | Why it matters for jobs |
|---|-------|----------------|-------------------------|
| 01 | [Fundamentals](01-fundamentals/) | syntax, types, slices, maps, structs, interfaces, errors, generics, iterators, JSON, testing, modules | Every interview starts here |
| 02 | [Concurrency](02-concurrency/) | goroutines, channels, select, sync, context, worker pools, errgroup, race detector | Go's killer feature; always asked |
| 03 | [SQL with SQLite](03-sql-sqlite/) | SQL itself, `database/sql`, transactions, joins, indexes, pagination, **sqlx**, **sqlc**, migrations | Go shops use raw SQL, not ORMs |
| 04 | [Web with stdlib](04-web-stdlib/) | `net/http`, routing, JSON, middleware, `slog`, HTTP client, graceful shutdown, `httptest` | Std lib is enough for production |
| 05 | [Capstone: taskapi](05-projects/taskapi/) | production REST API: layered layout, auth (bcrypt+JWT), sqlc, migrations, tests, Docker | Portfolio project for interviews |
| 06 | [Frameworks](06-frameworks/) | same API in **chi**, **Gin**, **Fiber** + comparison | Read any codebase you join |
| 07 | [Microservices](07-microservices/) | gRPC + protobuf, API gateway, tracing, docker-compose, retries, idempotency | How bigger companies split systems |
| 08 | [Other use cases](08-use-cases/) | CLI tools, TCP server, WebSockets, crawler, job worker | Go is also DevOps / infra / tooling |

Extra reading in [`docs/`](docs/):
- [industry-stack.md](docs/industry-stack.md) — what startups and big companies actually use, and why
- [postgres-later.md](docs/postgres-later.md) — moving from SQLite to PostgreSQL
- [interview-prep.md](docs/interview-prep.md) — Go, backend and SQL interview questions

## Why SQLite?

Zero setup: the database is a file and the driver (`modernc.org/sqlite`) is pure Go, so no Docker
or C compiler is needed. The SQL is written to be portable; `docs/postgres-later.md` shows the
handful of changes needed to switch to PostgreSQL, which is what most Go jobs use in production.

## Tooling used

| Tool | How you get it |
|------|----------------|
| Go 1.27.1+ | https://go.dev/dl |
| `sqlc`, `goose`, `buf` | pinned in `go.mod` as tools → `go tool sqlc ...` (no global install) |
| `golangci-lint` | optional locally (https://golangci-lint.run), always runs in CI |
| Docker | only for tracks 05 (image build) and 07 (docker-compose) |

## Resources

- [Go by Example](https://gobyexample.com/) — the style the fundamentals follow
- [Effective Go](https://go.dev/doc/effective_go) and [Go Code Review Comments](https://go.dev/wiki/CodeReviewComments)
- [Go release notes](https://go.dev/doc/devel/release) — what changed in each version
- [SQLBolt](https://sqlbolt.com/) — interactive SQL practice
