# 04 - Web with the standard library

Everything you need to build and operate an HTTP service using only `net/http`, `encoding/json`,
`log/slog` and friends. No framework, no third-party packages: Go's standard library is enough for
production, and every framework (chi, Gin, Echo, Fiber) builds on the ideas in these lessons.
Each lesson is a folder with a heavily commented `main.go` (read the header comment first, then run it)
and a `main_test.go` that shows how to test that kind of code with `httptest`.

## Lessons

| # | Folder | Topic |
|---|--------|-------|
| 01 | `01server` | `Handler` vs `HandlerFunc`, `ServeMux`, `ResponseWriter` order (headers, status, body), `http.Server` timeouts, why bare `ListenAndServe` is unsafe |
| 02 | `02routing` | Go 1.22+ patterns: methods, `{id}`, `{path...}`, `{$}`, hosts, precedence, automatic 404/405, query params, route groups with `StripPrefix` |
| 03 | `03json-api` | Task CRUD: `MaxBytesReader`, strict decoding, validation errors, one error envelope, status codes, `uuid.NewV7`, `RWMutex` |
| 04 | `04middleware` | `func(Handler) Handler`, chain order, request ID, `slog` access log, panic recovery, CORS, CSRF (`CrossOriginProtection`), `TimeoutHandler`, bearer auth |
| 05 | `05client` | tuned `http.Client`/`Transport`, connection reuse, drain + close, retries with backoff + jitter, `Retry-After`, idempotency |
| 06 | `06graceful` | `signal.NotifyContext`, `Shutdown`, in-flight requests, `BaseContext`, readiness/liveness for Kubernetes |
| 07 | `07config` | 12-factor config (defaults < env < flags), validation, secrets that cannot leak (`slog.LogValuer`), no globals, text vs JSON logs |
| 08 | `08structure` | mini production layout: `handler -> service -> store`, consumer-defined interfaces, error-to-status mapping in one place, fakes in tests |

Lesson 08 is the preview of the capstone project (`05-projects/taskapi`).

## Running

Run from the repository root. By default every lesson starts its own server on a random port, calls it
with an `http.Client`, prints what happened and **exits by itself**. Add `-serve` (lessons 01-04, 06, 07, 08)
to run a real server on `:8080` until Ctrl-C so you can use `curl`. Lesson 05 runs a client against a flaky local test server, so it needs no `-serve`.

```sh
go run ./04-web-stdlib/01server                 # scripted demo, exits
go run ./04-web-stdlib/01server -serve          # real server on :8080

# curl cheat-sheet (each lesson's header comment has more)
curl -i localhost:8080/hello                                          # 01 (-i shows status + headers)
curl -i -X DELETE localhost:8080/tasks/42                             # 02 -> 405 with Allow header
curl -i -X POST localhost:8080/tasks -d '{"title":"learn Go"}'        # 03 -> 201 + Location
curl -i -H 'Authorization: Bearer s3cret' localhost:8080/admin/stats  # 04
curl -i localhost:8080/readyz                                         # 06
curl -i -X POST localhost:8080/users -d '{"email":"a@b.co","name":"Ada"}'   # 08

# Tests
go vet ./04-web-stdlib/...
go test -race ./04-web-stdlib/...
```

Testing helpers used throughout:
- `httptest.NewRecorder()` + `httptest.NewRequest(...)` to call a handler directly (no network, fastest).
- `httptest.NewTestServer(t, handler)` (Go 1.27) for a full client-to-server test. It uses an in-memory
  network and closes itself when the test ends. Always send requests through `ts.Client()`.
- `httptest.NewServer(handler)` is the classic loopback server (use `defer ts.Close()`); the lessons use it in demos.

## HTTP status code cheat-sheet for APIs

| Code | Name | Use it when |
|------|------|-------------|
| 200 | OK | Successful `GET`, or `PUT`/`PATCH`/`POST` returning a body |
| 201 | Created | A `POST` created a resource. Send a `Location` header and usually the new resource |
| 202 | Accepted | Work was queued and will finish later (return a status URL) |
| 204 | No Content | Success with nothing to return: `DELETE`, some `PUT`s |
| 301 / 308 | Moved Permanently / Permanent Redirect | URL changed for good (308 keeps the method) |
| 304 | Not Modified | Conditional `GET` with `ETag` / `If-None-Match` matched |
| 400 | Bad Request | Malformed syntax: broken JSON, unknown field, bad query param |
| 401 | Unauthorized | Not authenticated (missing/invalid credentials). Send `WWW-Authenticate` |
| 403 | Forbidden | Authenticated but not allowed (also CSRF rejection) |
| 404 | Not Found | Resource does not exist (also used to hide existence from unauthorised callers) |
| 405 | Method Not Allowed | Path exists, method does not. Must send `Allow` (`ServeMux` does) |
| 409 | Conflict | Request conflicts with current state: duplicate unique value, version mismatch |
| 410 | Gone | Existed once, intentionally removed |
| 413 | Content Too Large | Body exceeds your limit (`MaxBytesReader`) |
| 415 | Unsupported Media Type | Wrong `Content-Type` |
| 422 | Unprocessable Content | Well-formed JSON that fails validation (return field errors) |
| 429 | Too Many Requests | Rate limited. Send `Retry-After` |
| 500 | Internal Server Error | Your bug. Log details, tell the client nothing specific |
| 502 | Bad Gateway | Proxy/gateway got a bad answer from upstream |
| 503 | Service Unavailable | Overloaded, starting up, or draining. `Retry-After` optional. Readiness probes use it |
| 504 | Gateway Timeout | Upstream did not answer in time |

Rule of thumb: **4xx = the caller must change something, 5xx = the server is at fault** (and a retry may help).
400 vs 422 is a style choice; pick one convention per API and document it (this repo: 400 for syntax, 422 for validation).

## REST design rules

1. **Nouns, not verbs, plural.** `GET /tasks`, `POST /tasks`, `GET /tasks/{id}`. Not `/getTask` or `/task/create`.
   The HTTP method is the verb. Actions that are not CRUD can be sub-resources (`POST /tasks/{id}/complete`).
2. **Nest only to express ownership**, and only one or two levels: `/users/{id}/orders`. Deeper than that, give the child its own top-level route.
3. **Use methods by their semantics:**

   | Method | Safe (no side effects) | Idempotent (same result if repeated) | Typical use |
   |--------|:---:|:---:|---|
   | GET | yes | yes | read |
   | HEAD / OPTIONS | yes | yes | metadata / CORS preflight |
   | PUT | no | yes | replace a whole resource (or create at a known id) |
   | DELETE | no | yes | remove (second call may be 404, state is still "gone") |
   | PATCH | no | not guaranteed | partial update |
   | POST | no | **no** | create, or any non-idempotent action |

   Idempotency decides what clients may retry automatically (lesson 05). Make `POST` safely retryable with an `Idempotency-Key` header.
4. **PUT vs PATCH.** `PUT` sends the complete representation and replaces the resource (missing fields reset). `PATCH` sends only the changes.
   In Go decode `PATCH` bodies into pointer fields (`*string`, `*bool`) so "absent" differs from "zero value" (lesson 03).
5. **Pagination from day one** for any list: prefer cursor/keyset (`?limit=50&after=<id>`) for large or changing data; offset (`?limit=&offset=`) is simpler but slow on deep pages and unstable under inserts. Cap `limit`, return a stable order, and wrap lists in an object (`{"tasks":[...],"next_cursor":"..."}`) so you can add fields later.
6. **Filtering and sorting** via query params (`?done=true&sort=-created_at`). Validate and whitelist them.
7. **Versioning.** Keep changes backwards compatible as long as possible (add fields, never remove or rename). When you must break: version in the path (`/api/v1/...`, easiest to route, cache and debug) or in a header. Mount each version with its own sub-mux (lesson 02).
8. **One error shape** for every failure, with a stable machine-readable `code` and a human `message` (lesson 03). Never leak stack traces or SQL.
9. **Consistent JSON:** `snake_case` or `camelCase`, pick one; timestamps in RFC 3339 UTC; ids as strings; never `null` for lists (use `[]`).
10. **Be strict on input, predictable on output:** size limits, `DisallowUnknownFields`, validate before touching the database, return `201` + `Location` on create.

## Interview questions

1. **What is `http.Handler`, and how does `http.HandlerFunc` relate to it?**
   `Handler` is an interface with one method, `ServeHTTP(ResponseWriter, *Request)`. `HandlerFunc` is a function type that has that method (it calls itself), so any `func(w, r)` can be used as a `Handler`. Muxes, middleware and whole routers are just Handlers wrapping Handlers.

2. **Why should you not use `http.ListenAndServe` directly in production?**
   It builds an `http.Server` with no timeouts, so slow or stalled clients (slowloris) can hold goroutines and file descriptors forever. Create your own `http.Server` with `ReadHeaderTimeout`, `ReadTimeout`, `WriteTimeout`, `IdleTimeout`, and use your own mux instead of the global `DefaultServeMux`.

3. **What happens if you call `w.Header().Set` after `w.Write`?**
   Nothing useful: the first `Write` (or `WriteHeader`) sends the status line and headers, so later header changes are ignored. The order is headers, then `WriteHeader`, then body. A second `WriteHeader` logs "superfluous response.WriteHeader call".

4. **How does routing work in Go 1.22+ `ServeMux`, and what resolves conflicts?**
   Patterns can include a method, host, wildcards (`{id}`, `{path...}`, `{$}`), read with `r.PathValue`. The most specific pattern wins regardless of registration order; if two patterns overlap and neither is more specific, registration panics. A matched path with the wrong method yields 405 + `Allow`; no match yields 404.

5. **How do you write middleware, and why does order matter?**
   A function `func(http.Handler) http.Handler` that returns a handler which runs code around `next.ServeHTTP`. With `chain(h, A, B, C)` = `A(B(C(h)))`, A is outermost. Request ID and logging go outside the panic recoverer so they see the 500; CORS goes before auth so preflights are not rejected; auth wraps only the routes that need it.

6. **How do you read the status code a handler wrote, for logging?**
   Wrap the `ResponseWriter` in a struct that records `WriteHeader`/`Write` calls. Also implement `Unwrap() http.ResponseWriter` so `http.ResponseController` can still reach `Flush`, `Hijack` and deadlines on the real writer; without it streaming and WebSockets silently break.

7. **Explain graceful shutdown in Go.**
   Cancel on SIGTERM/SIGINT with `signal.NotifyContext`; flip readiness to failing so load balancers stop routing; call `srv.Shutdown(ctx)` with a timeout, which closes listeners and idle connections and waits for active requests; `Serve` returns `ErrServerClosed` immediately, so `main` must wait for `Shutdown` to return. If the timeout expires, cancel the base context and `srv.Close()`. Kubernetes' `terminationGracePeriodSeconds` must exceed drain delay + shutdown timeout.

8. **What goes wrong with an `http.Client` in production, and how do you fix it?**
   `http.DefaultClient` has no timeout; creating a client per request discards the connection pool; not reading and closing `resp.Body` leaks or prevents reuse of connections; the default of 2 idle connections per host throttles busy services. Use one shared client with `Timeout`, a tuned `Transport` (`MaxIdleConnsPerHost`), `NewRequestWithContext`, `defer resp.Body.Close()` after draining, and check `StatusCode` yourself (a 500 is not an `error`).

9. **When is it safe to retry an HTTP request?**
   When the operation is idempotent (GET, PUT, DELETE, or POST with an idempotency key) and the failure is transient (network error, 429, 502/503/504). Use exponential backoff with jitter, honour `Retry-After` (capped), limit attempts, and stop when the context is done. Never retry other 4xx.

10. **How do you structure a Go web service so it is testable?**
    Layers `handler -> service -> store`, each depending on a small interface declared by its consumer (`accept interfaces, return structs`). Build everything in one composition root (`main`) with constructor injection, no global state. Domain errors travel up with `%w` and are mapped to HTTP status in one place. Handlers are tested with `httptest.NewRecorder` and a fake service; services with a fake store; a thin end-to-end test checks the wiring.

More questions across the whole repo: [`docs/interview-prep.md`](../docs/interview-prep.md).
