# 08 - Other use cases for Go

Backend web APIs are only one thing Go is used for. Go compiles to a single static binary, starts in
milliseconds, handles huge numbers of concurrent connections with goroutines and cross-compiles
trivially. That makes it the default language for command-line tools, infrastructure, networking
software and background workers. Each lesson here is a small, complete program you can read in one
sitting and run with `go run`.

By default every lesson is **self-contained and exits by itself in a few seconds**: servers and clients
run inside the same process on random ports, files go to temp directories. Each lesson also has a flag
(`-serve`, `-db`, `-url`...) for "real" mode.

## Lessons

| # | Folder | What you build | Real-world examples of Go in this space |
|---|--------|----------------|------------------------------------------|
| 01 | `01cli` | `tasks` CLI: `flag` subcommands, per-command `--help`, exit codes, stdout vs stderr, piped stdin, atomic JSON file storage, testable `run(args, stdout, stderr) int` | **Docker, Kubernetes (`kubectl`), Terraform, Helm, GitHub CLI (`gh`), Hugo**: most cloud-native tooling is Go. Big CLIs use cobra or urfave/cli; stdlib `flag` is enough for small tools |
| 02 | `02tcp-chat` | raw `net` TCP chat: goroutine per connection, a hub goroutine instead of locks, `bufio` line framing, nicknames, read/write deadlines, graceful close | **Caddy, Traefik, Envoy-style proxies and load balancers**, NATS (messaging), CockroachDB/Vitess (database wire protocols), game servers, Redis-compatible servers |
| 03 | `03websocket` | WebSocket chat with `coder/websocket`: embedded HTML UI (`embed.FS`), per-client send buffer with slow-consumer drop, ping keepalive, origin check, shutdown with close codes | **Discord-like chat and gateways**, live dashboards, collaborative editing, notifications, Centrifugo (Go real-time server) |
| 04 | `04crawler` | concurrent crawler: errgroup worker limit, mutex-guarded visited set, depth limit, same-host only, per-request timeout, `x/time/rate` politeness, robots.txt, HTML link extraction | **Colly** scrapers, link checkers, uptime and API probes, sitemap generators, Prometheus-style scrapers that poll thousands of targets |
| 05 | `05jobqueue` | SQLite-backed job queue: atomic claim (`UPDATE ... RETURNING`), retries with exponential backoff, dead letters, visibility-timeout recovery, fencing, graceful drain | **Sidekiq/Celery-like queues**: River (Postgres), Asynq and Machinery (Redis), Temporal workflows, SQS/Pub-Sub consumers |
| 06 | `06fileproc` | streaming 200k-row CSV pipeline: batched fan-out to workers, local aggregation, fan-in merge, sequential vs concurrent timing, memory comparison, pprof flags | **ETL and data jobs**, log processors, report generators, importers for billing/product feeds, Telegraf and Vector-style data shippers |
| 07 | `07pprof` | profiling basics: `runtime/pprof` CPU and heap profiles, `net/http/pprof` endpoints, `b.Loop` benchmarks (`+=` vs `strings.Builder`), `-benchmem`, escape analysis | **Continuous profiling** (Pyroscope, Datadog, Google Cloud Profiler); the Go team's own PGO; every performance investigation in a Go service |

## Running

Run everything from the repository root.

```sh
go run ./08-use-cases/01cli                       # scripted demo, exits
go run ./08-use-cases/01cli add -h                # help for one subcommand
go run ./08-use-cases/01cli add "learn Go"        # real use (stored under your user config dir)
go run ./08-use-cases/01cli -file /tmp/t.json list -json

go run ./08-use-cases/02tcp-chat                  # 3 in-process clients chat, then exit
go run ./08-use-cases/02tcp-chat -serve :9000     # real server; connect with: nc localhost 9000

go run ./08-use-cases/03websocket                 # in-process websocket clients, exits
go run ./08-use-cases/03websocket -serve :8080    # open http://localhost:8080 in two browser tabs

go run ./08-use-cases/04crawler                   # crawls an in-process site of 15 pages
go run ./08-use-cases/04crawler -url https://go.dev -depth 1 -rate 2 -workers 3   # polite real crawl

go run ./08-use-cases/05jobqueue                  # 20 jobs, retries, dead letters, crash recovery
go run ./08-use-cases/05jobqueue -v               # plus debug log of retries and dead letters
go run ./08-use-cases/05jobqueue -db /tmp/q.db -enqueue 10   # then, in another terminal:
go run ./08-use-cases/05jobqueue -db /tmp/q.db -work         # try kill -9 and restart

go run ./08-use-cases/06fileproc                  # 200k rows, sequential vs concurrent
go run ./08-use-cases/06fileproc -work 1          # cheap rows: concurrency barely helps (measure!)
go run ./08-use-cases/06fileproc -cpuprofile /tmp/cpu.out -memprofile /tmp/mem.out

go run ./08-use-cases/07pprof                     # profile, print the `go tool pprof` commands, exit
go run ./08-use-cases/07pprof -top                # ...and run `go tool pprof -top` for you
go run ./08-use-cases/07pprof -serve localhost:6060   # live pprof endpoints
go test -bench=. -benchmem ./08-use-cases/07pprof # benchmarks

# Quality checks for the whole track
go vet ./08-use-cases/...
go test -race ./08-use-cases/...
```

Lesson 03 uses `github.com/coder/websocket`, lesson 04 uses `golang.org/x/net/html`,
`golang.org/x/sync/errgroup` and `golang.org/x/time/rate`, lesson 05 uses the pure-Go SQLite driver
`modernc.org/sqlite`. Everything else is the standard library.

## Patterns that repeat across these lessons

- **`run(args, stdout, stderr) int`**: put the program in a function that takes its I/O and returns an exit code. `main` is one line.
- **One owner for shared state**: a hub goroutine (02, 03) or per-worker local maps (06) instead of a mutex around everything. When you do need a lock (04's visited set), keep the critical section tiny and atomic.
- **Bounded everything**: bounded workers (04, 05, 06), bounded channels (backpressure), bounded buffers per client (03), bounded line and body sizes (02, 04). Unbounded means "outage under load".
- **Timeouts and deadlines on all I/O**: `SetReadDeadline` (02), per-request `context.WithTimeout` (04), job timeouts and leases (05).
- **Graceful shutdown**: stop accepting work, let in-flight work finish, then wait (`WaitGroup` or `errgroup`) before returning.
- **Errors are data in batch jobs**: record the failure for one item and continue (04, 05, 06) instead of aborting everything.
- **Prove concurrency code with `-race` and with a test that compares it to the simple version** (06).

## Interview questions

1. **Why can a Go TCP server use one goroutine per connection, when that design is considered wasteful in languages with OS threads?** A goroutine costs a few KB of stack and the runtime multiplexes blocked goroutines onto epoll/kqueue, so blocking reads are cheap. You write simple sequential code per connection instead of callback state machines.
2. **TCP is a stream. What does that mean for your protocol code, and how do you handle it?** One `Write` may arrive as several `Read`s and several writes may arrive together, so you define message boundaries yourself (newline delimiters read via `bufio.Scanner`/`Reader`, or a length prefix) and cap message size so one client cannot make you buffer forever.
3. **In a chat hub, how do you stop one slow client from blocking everyone? Why use a hub goroutine instead of a mutex-protected map?** Give each client a bounded send channel and use a non-blocking send; if the buffer is full, drop or disconnect that client. A single goroutine owning the state means there is nothing to race on and one place to apply policy. Writes to sockets also need deadlines.
4. **What is cross-site WebSocket hijacking and how do you prevent it? What else must you handle for WebSockets in production?** Browsers let any page open a WebSocket to your host and attach cookies, so verify the `Origin` header (coder/websocket does by default; list extra origins explicitly). Also: ping/pong keepalive for dead peers and idle-timeout proxies, message size limits, writes with timeouts, and closing sockets yourself on shutdown (`http.Server.Shutdown` does not close hijacked connections). Scaling to several replicas needs pub/sub (Redis, NATS) behind the hub.
5. **How do you limit concurrency in a crawler or any fan-out job, and what deadlock can `errgroup.SetLimit` cause?** `errgroup.SetLimit(n)`, a semaphore channel, or a fixed worker pool. If a task running in a limited group launches more tasks into the same group while all slots are held by waiting parents, nobody can proceed: process work level by level, or use a queue with workers. Add a rate limiter (`x/time/rate`) for politeness and a per-request `context` timeout.
6. **Delivery semantics of a job queue: why is "exactly once" a trap, and what do handlers have to do?** A worker can finish the side effect and crash before recording success, so jobs run at-least-once. Handlers must be idempotent (idempotency keys, unique constraints, upserts). Explain visibility timeouts, retries with exponential backoff and jitter, dead-letter queues, and fencing so a late worker cannot overwrite a newer attempt.
7. **How do you let many workers take jobs from one database table without two taking the same job?** Claim atomically in one statement (`UPDATE ... WHERE id = (SELECT ... LIMIT 1) RETURNING ...`). In PostgreSQL use `SELECT ... FOR UPDATE SKIP LOCKED` so workers skip rows others have locked instead of waiting. Mention the transactional-outbox benefit of enqueuing in the same transaction as your data.
8. **A Go service is slow and memory keeps growing. Walk me through diagnosing it.** Reproduce and measure first. Expose `net/http/pprof` on an internal port (never publicly); take a CPU profile (`go tool pprof -http`), read flat vs cumulative time and the flame graph; take a heap profile (`inuse_space` for leaks, `alloc_space` for churn), compare two snapshots over time; check goroutine counts for leaks; use mutex/block profiles for contention and `go tool trace` for scheduler/GC behaviour. For micro-optimisations write a benchmark (`b.Loop`, `-benchmem`), compare runs with `benchstat`, and check `-gcflags=-m` when allocations from escaping values matter.

Bonus: *When is the stdlib `flag` package enough for a CLI and when do you reach for cobra? How do you make a CLI testable and script-friendly?* (Flat flags and a few subcommands versus nested commands, completion and generated docs; a `run()` function with injected writers, meaningful exit codes, results on stdout and diagnostics on stderr, `-json` output, and reading piped stdin.)
