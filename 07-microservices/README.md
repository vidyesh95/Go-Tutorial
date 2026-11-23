# 07 - Microservices

Three small services that talk to each other the way real ones do: **gRPC** for the synchronous
call, **HTTP/JSON** at the edge through an **API gateway**, a **transactional outbox** for
asynchronous events, **idempotency keys**, **deadlines and retries**, and **distributed tracing**
with OpenTelemetry. Everything runs with one command, no Docker required.

```bash
go run ./07-microservices/cmd/demo        # whole system in one process, scripted scenario (~1s)
go run ./07-microservices/cmd/demo -v     # same, plus every service's structured logs
```

> Prerequisite: lessons 03 (SQL), 04 (web) and 05 (taskapi). Read `05-projects/taskapi` first if
> `http.Server`, middleware or graceful shutdown are unfamiliar. They are reused here without
> re-explaining.

## Architecture

```
                         public                      private network (nobody outside can reach these)
                           |
  client ---HTTP/JSON---> gateway :8080 ---HTTP/JSON---> orders-svc :8081 ---gRPC (HTTP/2)---> users-svc :9090
 (curl, browser,            |                              |        |                              |
  mobile app)               |  X-Request-ID                |        |  GetUser (deadline,          |  users.db
                            |  traceparent                 |        |  retry on Unavailable)       |  (SQLite, owned
                            |  per-route timeout           |        |                              |   by users-svc)
                            |  /healthz aggregate          |        |
                                                           |        +--- orders.db (SQLite, owned by orders-svc)
                                                           |               orders, idempotency_keys, outbox
                                                           |
                                                           +-- outbox relay (goroutine) --> EventBus
                                                                                            (log here; Kafka/NATS in real life)
```

| Service | Protocol | Owns | Responsibility |
|---|---|---|---|
| `gateway` | HTTP in, HTTP out | nothing | routing, request IDs, trace propagation, timeouts, aggregate health |
| `orders-svc` | HTTP/JSON in, gRPC out | `orders.db` | create/read orders; idempotency; outbox events |
| `users-svc` | gRPC | `users.db` | create/read users; health service; reflection |

### What happens on `POST /api/orders`

1. **gateway** gives the request an `X-Request-ID`, starts a trace span, applies the route's 3s
   timeout, rewrites `/api/orders` to `/orders`, and proxies it.
2. **orders-svc** requires an `Idempotency-Key`. If it has seen the key (same body) it answers from
   its own database and stops here.
3. Otherwise it calls **users-svc** `GetUser` over gRPC with a 1s per-attempt deadline, retrying
   (with backoff) only on `Unavailable`. `NotFound` becomes HTTP 422, `Unavailable` /
   `DeadlineExceeded` become 503.
4. In **one SQL transaction** it inserts the order, the idempotency record and an outbox event.
5. Later, the **relay** goroutine publishes the outbox event to the bus and marks it sent.

## Layout

```
07-microservices/
  proto/users/v1/users.proto   the gRPC contract (source of truth)
  buf.yaml, buf.gen.yaml       buf lint + code generation config
  gen/users/v1/                GENERATED Go code (committed; do not edit)
  users-svc/                   main.go + userssvc/ (store, gRPC server, interceptors, Run)
  orders-svc/                  main.go + orderssvc/ (API, store, outbox relay, gRPC client, Run)
  gateway/                     main.go + gatewaysvc/ (reverse proxy, health aggregation)
  internal/                    shared by this track only: telemetry, sqlitedb, svc (lifecycle, middleware)
  cmd/demo/                    runs all three in-process and plays the scenario
  Dockerfile, docker-compose.yml
```

Each service is a library package (`Run(ctx, listener, config, logger, tracerProvider)`) plus a
thin `main.go` that reads env vars. That split is why `cmd/demo` can start all three in one process
on random ports, and why the tests can too. **Real teams give each service its own repo or Go
module** and share only the `.proto` files; one module here is just for convenience.

## Running it

### 1. The demo (recommended first)

`go run ./07-microservices/cmd/demo` prints, in order: a user created over gRPC and its error codes;
an order through the gateway (201); the same `Idempotency-Key` replayed (200, same order, no second
users-svc call); the same key with a different body (422); an unknown user (422); the outbox events
the relay delivered (exactly one); the 7-span distributed trace across client, gateway, orders-svc
and users-svc; then users-svc is **stopped** and the next order returns 503 after retries, while
replaying an old key still works.

### 2. Three terminals

```bash
ADDR=:9090 DB_PATH=/tmp/users.db   go run ./07-microservices/users-svc
ADDR=:8081 DB_PATH=/tmp/orders.db USERS_ADDR=localhost:9090 go run ./07-microservices/orders-svc
ADDR=:8080 ORDERS_URL=http://localhost:8081 go run ./07-microservices/gateway
```

Add `TRACE_STDOUT=1` to any of them to print that service's spans as JSON.

### 3. Docker Compose (separate containers, volumes, healthchecks)

```bash
docker compose -f 07-microservices/docker-compose.yml up --build
docker compose -f 07-microservices/docker-compose.yml down        # -v also deletes the DB volumes
```

One `Dockerfile` builds any service via `--build-arg SERVICE=...`: multi-stage, `CGO_ENABLED=0`
(modernc SQLite is pure Go), distroless `nonroot` runtime. The runtime image has no shell, so the
healthchecks run the binary itself: `/app -healthcheck`. Compose gives `users-svc` and `orders-svc`
**separate volumes**, a visible expression of "database per service".

### 4. Poking the services

```bash
# gRPC: users-svc has reflection enabled, so grpcurl needs no .proto file
# (install: brew install grpcurl)
grpcurl -plaintext localhost:9090 list
grpcurl -plaintext localhost:9090 describe users.v1.UsersService
grpcurl -plaintext -d '{"name":"Ada","email":"ada@example.com"}' localhost:9090 users.v1.UsersService/CreateUser
grpcurl -plaintext -d '{"id":"<id from above>"}'                 localhost:9090 users.v1.UsersService/GetUser
grpcurl -plaintext localhost:9090 grpc.health.v1.Health/Check

# HTTP through the gateway
curl -i -X POST localhost:8080/api/orders -H 'Idempotency-Key: k1' \
     -d '{"user_id":"<id>","item":"book","quantity":2}'
curl -i -X POST localhost:8080/api/orders -H 'Idempotency-Key: k1' \
     -d '{"user_id":"<id>","item":"book","quantity":2}'     # replay: 200 + Idempotent-Replayed: true
curl -i localhost:8080/api/orders/<order id>
curl -i localhost:8080/healthz                              # aggregate; /livez is process-only
```

### 5. Changing the contract

```bash
cd 07-microservices
go tool buf lint && go tool buf generate     # regenerates gen/
# once the proto is committed, CI runs: go tool buf breaking --against '.git#branch=main,subdir=07-microservices'
```

`buf`, `protoc-gen-go` and `protoc-gen-go-grpc` are pinned in `go.mod`'s `tool` block, so there is
nothing to install and everyone generates identical code.

### 6. Tests

```bash
go test -race ./07-microservices/...
```

* **users-svc**: a real gRPC server over an in-memory `bufconn` listener: status codes, health service, panic recovery.
* **orders-svc**: handlers with a **fake `UsersClient`** (idempotency, replay with users-svc down, concurrent same key,
  error mapping, nothing written on failure), the outbox relay (publish, broker down, stop on cancel), and the gRPC
  client's retry rules with a scripted fake.
* **gateway**: `httptest` backends: path rewrite, header forwarding, request ID, trace propagation, 502/504, health aggregation.

## Concepts

### Service boundaries and a database per service

A service is a unit that owns **one business capability and its data**. Only users-svc opens
`users.db`; orders-svc must *ask* (gRPC). Why it matters: if two services share tables, a column
rename in one breaks the other and you have a distributed monolith, all the cost of microservices
with none of the independence.

The price: **no cross-service transactions or foreign keys**. `orders.user_id` cannot reference
`users.id`, so orders-svc validates it by calling users-svc, and a user deleted later leaves
dangling references that you handle in application logic (events, sagas, soft deletes). Draw
boundaries around things that change together (high cohesion) and rarely need to be joined.

### Synchronous (gRPC/HTTP) vs asynchronous (events)

| | Synchronous call | Asynchronous event |
|---|---|---|
| Example here | orders-svc -> users-svc `GetUser` | order created -> outbox -> bus |
| Caller needs the answer now | yes | no |
| Coupling | temporal: callee must be up *now* | callee can be down; it catches up |
| Failure mode | error/timeout propagates up | message waits in the queue |
| Use for | validation, queries, "do X and tell me the result" | notifications, projections, "this happened" |

Chains of synchronous calls multiply failure: if A calls B calls C and each is 99.9% available,
A is ~99.7% available, and latencies add. Prefer events when the caller does not truly need an answer.

### Timeouts and deadline propagation

**Every network call needs a timeout**; the default in most clients is "forever". Without one, a
single slow dependency makes callers' goroutines, connections and memory pile up until *they* fail:
a cascading failure. Here (`orders-svc/orderssvc/usersclient.go`) every attempt uses
`context.WithTimeout`. gRPC puts that deadline on the wire, so users-svc's own `ctx` has the same
deadline and can abandon work nobody is waiting for. The gateway's per-route timeout cancels the
upstream request, so deadlines flow **down** the chain. Rule of thumb: timeouts shrink as you go
deeper (gateway 3s > orders per-call 1s), otherwise the caller gives up first and the callee keeps
working for nothing.

### Retries and idempotency

Retrying turns transient failures (a pod restarting, a dropped connection) into non-events, but it
has rules, all visible in `GRPCUsers.GetUser`:

* Retry only errors that mean "probably didn't run" (`Unavailable`), never `NotFound` or `InvalidArgument` (the answer won't change).
* Don't retry `DeadlineExceeded`: the time budget is spent and extra load on a struggling service makes it worse (a **retry storm**).
* Bound the attempts, **back off exponentially with jitter** so clients don't retry in lockstep, and stop when the context is done.
* Retry **only idempotent operations**. `GetUser` is a read. `POST /orders` is not naturally idempotent: a retry after a timeout could create two orders. The fix is the **`Idempotency-Key`** header: the client picks a unique key per *logical* operation; the server stores `key -> (request hash, result)` and returns the stored result for repeats. A different body under the same key is a client bug (422). Concurrent duplicates are serialised by the transaction (and the table's primary key).

### The transactional outbox and at-least-once delivery

The problem: "save the order **and** publish `order.created`". A database and a broker can't share a
transaction (dual write). Save first and crash before publishing: the event is lost. Publish first
and the save fails: a phantom event. The outbox pattern: write the event to an `outbox` table **in the
same transaction** as the order, so both happen or neither does; a **relay** later reads unsent rows,
publishes them, and marks them sent.

* Delivery is **at-least-once**: a crash between "published" and "marked sent" means a republish after restart. **Consumers must be idempotent** (de-duplicate on `event_id`). "Exactly-once" delivery doesn't exist; at-least-once + idempotent consumers gives exactly-once *effects*.
* Order is preserved (relay publishes by outbox id and stops the batch on failure).
* `EventBus` is the seam: `LogBus` logs. With **Kafka** you'd implement `Publish` with a producer (key = order id so one order's events stay ordered; wait for broker ack); with **NATS JetStream** a `js.Publish` with a message-ID header for server-side dedup.
* Polling is the simple relay. Production alternatives: **CDC** (Debezium tailing the WAL), or multiple relay instances claiming rows with `SELECT ... FOR UPDATE SKIP LOCKED` (Postgres).
* The inverse, the **inbox** pattern, records processed event IDs on the consumer side.

### Circuit breakers (explained, not implemented here)

Retries help with blips; for a *sustained* outage they make things worse, because every request
still waits out a full timeout and hammers the dead service. A **circuit breaker** wraps the
client and tracks recent failures in three states:

* **Closed**: normal, calls pass through, failures are counted.
* **Open**: after N failures (or an error-rate threshold) calls **fail immediately** without touching the network, for a cool-down period. Callers get a fast 503 and the dependency gets breathing room.
* **Half-open**: after the cool-down, let a few trial calls through; success closes the circuit, failure re-opens it.

In Go you'd use `github.com/sony/gobreaker` or put it in the platform (Envoy/Istio outlier detection).
It would sit inside `GRPCUsers.GetUser` around the whole retry loop. Pair it with a **fallback**
(cached value, degraded response) and **bulkheads** (separate connection/goroutine pools per
dependency) so one bad dependency can't starve the rest.

### Distributed tracing

One user action fans out across services; logs alone can't tell you where the 800ms went. A **trace**
is a tree of **spans** (one per unit of work: an HTTP handler, an RPC, a DB query) sharing a trace
ID. Propagation is the trick: the caller injects a `traceparent` header (W3C Trace Context) /
gRPC metadata entry, the callee extracts it and parents its spans to it.

Here that is two lines of wiring per hop: `otelhttp.NewHandler` / `otelhttp.NewTransport` for HTTP,
`otelgrpc.NewServerHandler` / `NewClientHandler` for gRPC (see `internal/telemetry`). The demo prints
the resulting tree. We export to stdout; real systems send OTLP to a collector feeding Jaeger,
Tempo, Honeycomb or Datadog. Pair tracing with the `X-Request-ID` in every log line, **metrics**
(RED: rate, errors, duration per endpoint) and structured logs: the three pillars of observability.

### What an API gateway is for

A single, stable public entry point that handles **cross-cutting concerns once**: routing (and
hiding internal topology), authentication/authorisation, rate limiting, TLS, request IDs, tracing,
timeouts, CORS, aggregate health. Ours does routing with `httputil.ReverseProxy` and its `Rewrite`
hook (the old `Director` is deprecated in Go 1.26; `Rewrite` runs after hop-by-hop and forwarding
headers are stripped, so clients can't spoof `X-Forwarded-*`), request IDs, trace propagation, a
per-route timeout (504 on expiry, 502 if the service is down) and `/healthz`.

Keep business logic **out**: a gateway that knows about orders couples every release to it. In
industry you'd typically run Envoy, Kong, Traefik, an Ingress controller or a cloud API gateway
rather than maintain your own; also consider **BFFs** (a gateway per client type: web, mobile).
Note the health split: `/healthz` aggregates downstreams for humans and dashboards, `/livez` is
what Docker/Kubernetes probe. Restarting the gateway because orders is down fixes nothing.
Likewise orders-svc's readiness checks only its own database, so a users-svc outage doesn't turn
every service "unhealthy" at once.

### When NOT to use microservices

Microservices trade *code complexity* for *operational and distributed-systems complexity*: network
failures, partial failure, eventual consistency, versioned contracts, tracing, deployment
pipelines, on-call across many services. That's worth it when you have:

* many teams who need to **deploy independently**;
* parts with very different **scaling or reliability** needs;
* a clear, stable domain split.

It is a poor trade for a small team, an unclear domain (you'll draw boundaries wrongly and pay for
every wrong one in cross-service changes), or a product still finding its shape. The usual advice:
start with a **well-structured modular monolith** (the layering in `05-projects/taskapi`), extract a
service when a concrete pain (team contention, scaling hot spot) justifies it. Signs you built a
distributed monolith: services must deploy together, share a database, or call each other in long
synchronous chains.

## Interview questions

1. **What is the "database per service" rule, and what do you lose by following it?** (No joins, FKs or ACID transactions across services; consistency becomes eventual and your code's job.)
2. **gRPC vs REST for service-to-service calls?** (Typed contract + codegen, HTTP/2 multiplexing, streaming, built-in deadlines/metadata, smaller payloads; vs universal tooling, browser/third-party friendliness, human-readable. Often gRPC inside, REST at the edge.)
3. **A client times out calling `POST /orders`. Can it safely retry? How do you make it safe?** (Not blindly: the first call may have succeeded. Idempotency key + stored result keyed by (key, request hash); same key different body is rejected.)
4. **Which gRPC errors would you retry, and how?** (`Unavailable`; not `DeadlineExceeded` or client errors; exponential backoff with jitter, capped attempts, respect ctx; only idempotent calls; mention retry storms and retry budgets.)
5. **Explain the dual-write problem and how the transactional outbox solves it.** (DB + broker can't share a tx; write event in the same tx, relay publishes; at-least-once; consumers dedupe.)
6. **Is exactly-once delivery possible?** (Not over an unreliable network; at-least-once + idempotent consumers = exactly-once processing effect.)
7. **How do deadlines propagate across services, and why should downstream timeouts be shorter than upstream ones?** (Context carries the deadline; gRPC sends `grpc-timeout`; otherwise callee keeps working after the caller gave up and retries amplify load.)
8. **What does a circuit breaker do and how does it differ from a retry?** (Retry handles transient faults; breaker stops calling a persistently failing dependency, fail fast, half-open probing; they compose, with the breaker around the retries.)
9. **How does distributed tracing work end to end?** (Trace/span IDs, context propagation via `traceparent`/metadata, instrumentation at each hop, exporters/collectors, sampling; vs logs and metrics.)
10. **When would you choose a modular monolith over microservices, and how would you extract a service later?** (Team/domain/scale reasons; keep module boundaries and interfaces clean, own tables per module, then move a module behind an API; strangler fig migration.)

Bonus: *Why should a readiness probe not call downstream services?* (Cascading failures.)
*Why does `ReverseProxy` use `Rewrite` instead of `Director`?* (Header sanitising and spoof-resistance.)
