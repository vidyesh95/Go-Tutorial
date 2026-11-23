# 06 - Web frameworks: chi, Gin, Fiber (and the stdlib baseline)

The same tiny **notes API** built four times. Nothing is shared between the folders, so you can
read them side by side or `diff` them:

```shell
diff stdlib/main.go chi/main.go
diff chi/main.go gin/main.go
diff gin/main.go fiber/main.go
```

| Folder | Stack | Try it |
|--------|-------|--------|
| [`stdlib/`](stdlib/) | `net/http` + Go 1.22 `ServeMux` | `go run ./06-frameworks/stdlib` |
| [`chi/`](chi/) | `github.com/go-chi/chi/v5` | `go run ./06-frameworks/chi` |
| [`gin/`](gin/) | `github.com/gin-gonic/gin` | `go run ./06-frameworks/gin` |
| [`fiber/`](fiber/) | `github.com/gofiber/fiber/v3` (fasthttp) | `go run ./06-frameworks/fiber` |

Each program, by default, sends demo requests to its own handler **in memory** (no port) and
prints the results, then exits. Add `-serve` to listen on `:8080` until Ctrl-C (with graceful
shutdown), then use `curl`:

```shell
go run ./06-frameworks/chi -serve
curl -i -X POST localhost:8080/api/v1/notes -H 'Content-Type: application/json' -d '{"title":"hello"}'
curl -i localhost:8080/api/v1/notes/1
```

Run the tests (they are the same four test suites; only the request helper differs):

```shell
go test -race ./06-frameworks/...
```

## The API (identical in all four)

| Method + path | Result |
|---------------|--------|
| `GET /api/v1/notes` | 200, JSON array |
| `POST /api/v1/notes` | 201 + `Location`; body `{"title","body"}`; title required, at most 100 chars, else **422** |
| `GET /api/v1/notes/{id}` | 200, or 404 / 400 (id not an integer) |
| `DELETE /api/v1/notes/{id}` | 204, or 404 / 400 |
| `GET /api/v1/boom` | panics on purpose: proves the recovery middleware returns a clean 500 |

Every error uses one envelope: `{"error":{"code":"validation_failed","message":"title is required"}}`.

Each version has: an in-memory store (`store.go`, same in all), request-logging middleware using
`log/slog`, panic recovery, a `/api/v1` route group, and graceful shutdown.

## Comparison

| | stdlib `net/http` | chi | Gin | Fiber v3 |
|---|---|---|---|---|
| Router style | `ServeMux`, patterns like `"GET /notes/{id}"` (Go 1.22+) | radix tree, `r.Get("/notes/{id}", h)`, `r.Route(...)` groups | radix tree (httprouter heritage), `r.GET("/notes/:id", h)`, `r.Group(...)` | tree router, Express-like `app.Get("/notes/:id", h)`, `app.Group(...)` |
| Handler signature | `func(http.ResponseWriter, *http.Request)` | same as stdlib | `func(*gin.Context)` | `func(fiber.Ctx) error` |
| Returns an error? | no | no | no | **yes**, goes to the app `ErrorHandler` |
| `net/http` compatible? | it *is* net/http | **yes**, handlers are plain `http.Handler` | server side yes (`Engine` is an `http.Handler`); handlers/middleware are Gin-typed, adapt with `gin.WrapH/WrapF` | **no**, built on fasthttp; bridge with the `adaptor` middleware package |
| Middleware ecosystem | write `func(http.Handler) http.Handler`; all of the Go ecosystem works | same type, so everything in the ecosystem works, plus `chi/middleware` | `gin.HandlerFunc` + `c.Next()`; large `gin-contrib` set | own `middleware/...` packages (recover, logger, cors, limiter, ...) |
| Binding / validation | manual: `json.Decoder` + your own checks | manual (same as stdlib) | built in: `ShouldBindJSON` + `binding:"required,max=100"` tags (go-playground/validator) | `c.Bind().Body(&x)` decodes by Content-Type; validation via `StructValidator` config or by hand |
| Performance notes | very good; more than enough for most services | essentially stdlib speed; tiny overhead | fast, low allocation router; its own `Context` | fastest in micro-benchmarks (fasthttp), but real services are bound by the DB/network, not the router |
| Typical users | teams that value zero dependencies; Go-core-minded shops | teams that want a router but stay on net/http | many existing codebases (very large user base) | teams coming from Node/Express; throughput-driven choices |
| Learning curve | lowest if you know Go; you write a few helpers yourself | very low (it *is* stdlib plus a router) | low; lots of helpers to learn | low if you know Express; but must learn fasthttp's rules |
| Gotchas | 404/405 replies are plain text unless you wrap the mux | `middleware.Recoverer` writes plain text (we wrote a JSON one) | use `ShouldBind*`, not `Bind*` (the latter writes its own 400); debug mode on by default | **values from `Ctx` are only valid inside the handler**; no `net/http` types; missing `Content-Type` makes `Bind().Body` silently decode nothing |

Also exists: **Echo** (`labstack/echo`) is a fourth popular framework, very close to Gin in
spirit (its handlers return `error` like Fiber, but it sits on net/http). Others you will meet:
gorilla/mux (archived once, now maintained again, but chi/stdlib replaced it in new code),
httprouter, Goa, and for RPC-first designs gRPC/Connect (see 07-microservices).

### What the frameworks actually save you

Looking at the four `main.go` files, the framework changes surprisingly little: routing syntax,
how you read a path parameter, how you write JSON, and how middleware is spelled. The business
logic (`store.go`, validation rules, status codes) is the same. That is why teams switch
frameworks rarely and why you should keep logic out of `*gin.Context` / `fiber.Ctx`.

## Port map: the same handler in four styles

`GET /api/v1/notes/{id}`, trimmed to the framework-specific lines:

```go
// stdlib  (mux: "GET /notes/{id}")
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil { writeError(w, 400, "invalid_id", "id must be an integer"); return }
	n, ok := a.store.Get(id)
	if !ok { writeError(w, 404, "not_found", "note not found"); return }
	writeJSON(w, 200, n)
}

// chi     (router: r.Get("/notes/{id}", api.get))  -- same signature, different param call
func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.Atoi(chi.URLParam(r, "id"))
	... // everything else is identical to stdlib
}

// gin     (router: v1.GET("/notes/:id", api.get))
func (a *API) get(c *gin.Context) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil { writeError(c, 400, "invalid_id", "id must be an integer"); return }
	n, ok := a.store.Get(id)
	if !ok { writeError(c, 404, "not_found", "note not found"); return }
	c.JSON(200, n)
}

// fiber   (router: v1.Get("/notes/:id", api.get))  -- note the returned error
func (a *API) get(c fiber.Ctx) error {
	id, err := strconv.Atoi(c.Params("id"))
	if err != nil { return writeError(c, 400, "invalid_id", "id must be an integer") }
	n, ok := a.store.Get(id)
	if !ok { return writeError(c, 404, "not_found", "note not found") }
	return c.Status(200).JSON(n)
}
```

Middleware (logging) in each style:

| | skeleton |
|---|---|
| stdlib / chi | `func(next http.Handler) http.Handler { return http.HandlerFunc(func(w, r) { ...; next.ServeHTTP(w, r); ... }) }` |
| Gin | `func(c *gin.Context) { ...; c.Next(); ... }` |
| Fiber | `func(c fiber.Ctx) error { ...; err := c.Next(); ...; return err }` |

Binding a JSON body: stdlib/chi `json.NewDecoder(r.Body).Decode(&req)`; Gin
`c.ShouldBindJSON(&req)`; Fiber `c.Bind().Body(&req)`.

## Which would I choose?

- **Default: stdlib, or chi.** Since Go 1.22 the stdlib mux handles methods and path wildcards,
  so for a small or medium API it is enough and has zero dependencies. Add chi when you want route
  groups, per-group middleware and a bigger middleware toolbox, while staying 100% `net/http`
  (so OpenTelemetry, rate limiters, auth libraries and `httptest` all just work).
- **Gin when your team already uses it.** It is mature, fast, and has the built-in binding/validation
  that many people like. Do not rewrite a working Gin service for fashion. For a new service,
  there is no strong reason to prefer it over chi, other than familiarity.
- **Fiber for Express-like familiarity or when raw throughput is a hard requirement.** Know the
  caveats: it is not `net/http` (adaptors, different middleware ecosystem), `Ctx` values must not
  outlive the handler (copy them, or enable `Immutable`), and the major-version jump from v2 to v3
  changed signatures (`*fiber.Ctx` became the `fiber.Ctx` interface). Benchmark your real
  workload before choosing it for speed: the database almost always dominates.
- In an interview, the winning answer is "stdlib/chi by default, I can work in whatever the
  team uses", plus knowing *why* they differ (the `net/http` compatibility question).

## Interview questions

1. **Since Go 1.22, why might you not need a router library at all?** What does
   `mux.HandleFunc("GET /notes/{id}", ...)` give you, and what is still missing (route groups,
   a middleware chain helper, custom 404/405 bodies)?
2. **What does it mean that chi handlers are "plain `http.Handler`s"?** Why does that make the
   middleware ecosystem larger than Gin's or Fiber's? How would you use `http.TimeoutHandler`
   with chi? With Gin? With Fiber?
3. **Fiber is built on fasthttp, not `net/http`. What are the consequences?** Mention `Ctx`
   reuse (why is storing `c.Params("id")` in a map dangerous, and how do you fix it?),
   middleware compatibility, HTTP/2, and the `adaptor` package.
4. **Explain `c.Next()` in Gin and Fiber versus `next.ServeHTTP` in stdlib middleware.** In which
   order do logging and recovery middleware need to be registered so the log shows the 500
   produced by a panic? What happens if they are swapped?
5. **Where does validation belong?** Compare Gin's `binding:"required,max=100"` tags with
   hand-written checks. Which status code do you return for malformed JSON (400) and for valid
   JSON with invalid fields (422), and why distinguish them? (Hint: look at how `gin/main.go`
   tells the two errors apart.)
6. **How do you do a graceful shutdown with each of these?** What is lost with `gin.Run(":8080")`
   or `app.Listen` without shutdown handling, and what do `http.Server.Shutdown` and
   `app.ShutdownWithContext` do with in-flight requests and keep-alive connections?
