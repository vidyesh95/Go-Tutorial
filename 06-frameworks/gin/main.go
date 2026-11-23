// Frameworks 3/4: the notes API with Gin (github.com/gin-gonic/gin).
//
// Gin is the most popular Go web framework by GitHub stars. It wraps net/http with its own
// context type and adds a lot of batteries.
//
// What is different from ../stdlib and ../chi:
//   - Handler signature is func(c *gin.Context). The Context carries the request, the
//     response writer, path params, a key/value store and helpers (c.JSON, c.Param,
//     c.ShouldBindJSON). Nothing is returned: you write the response and return, like net/http.
//   - gin.Engine implements http.Handler, so it plugs into http.Server, httptest, and the
//     same graceful shutdown code. But handlers and middleware are gin-typed: a plain
//     func(http.Handler) http.Handler cannot be used with r.Use directly. Wrap it with
//     gin.WrapH / gin.WrapF for handlers, or write a gin.HandlerFunc.
//   - Middleware is a gin.HandlerFunc that calls c.Next() to run the rest of the chain, and
//     c.Abort() to stop it. Code after c.Next() runs on the way out (we use that to log).
//   - Binding + validation is built in. c.ShouldBindJSON(&req) decodes JSON and checks the
//     `binding:"required,max=100"` struct tag (go-playground/validator). Use ShouldBind*, not
//     Bind*: Bind* writes its own 400 plain-text response and aborts, which breaks our
//     error envelope. Tags cannot trim whitespace, so we also trim after binding.
//   - gin.New() gives a bare engine. gin.Default() = New() + Logger() + Recovery(); we
//     build it by hand to use slog and a JSON recovery handler.
//   - Route groups: r.Group("/api/v1") returns a RouterGroup; it can carry its own middleware.
//   - Path params use ":id" syntax: r.GET("/notes/:id", ...) and c.Param("id").
//   - Gin runs in debug mode by default and prints every route; set gin.ReleaseMode in
//     production (and gin.TestMode in tests, as main_test.go does).
//
// Run:
//
//	go run ./06-frameworks/gin            # demo requests in memory, then exit
//	go run ./06-frameworks/gin -serve     # listen on :8080 until Ctrl-C
//
// In industry: Gin is everywhere, especially in Chinese tech companies and older Go codebases
// ("we picked Gin in 2018"). If your team already uses it, keep using it; it is fast enough
// and well supported. Just keep business logic out of *gin.Context so it can be tested and
// reused without Gin.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/go-playground/validator/v10"
)

// ---------- JSON helpers ----------

// gin.H is just map[string]any. A typed struct keeps the envelope identical across folders.
type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeError(c *gin.Context, status int, code, msg string) {
	c.JSON(status, errorBody{errorDetail{Code: code, Message: msg}})
}

// ---------- handlers ----------

type API struct {
	store *Store
}

func (a *API) list(c *gin.Context) {
	c.JSON(http.StatusOK, a.store.List())
}

// The struct tag IS the validation rule: required + at most 100 characters.
type createRequest struct {
	Title string `json:"title" binding:"required,max=100"`
	Body  string `json:"body"`
}

func (a *API) create(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 1<<20)

	var req createRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		// Two very different failures come out of one call: broken JSON (400) vs
		// well-formed JSON that fails the tags (422). The validator's error type tells them apart.
		if verrs, ok := errors.AsType[validator.ValidationErrors](err); ok {
			writeError(c, http.StatusUnprocessableEntity, "validation_failed", validationMessage(verrs[0]))
			return
		}
		writeError(c, http.StatusBadRequest, "bad_json", "request body must be valid JSON")
		return
	}
	title := strings.TrimSpace(req.Title)
	if title == "" { // "   " passes `required`; tags can't trim
		writeError(c, http.StatusUnprocessableEntity, "validation_failed", "title is required")
		return
	}
	n := a.store.Create(title, req.Body)
	c.Header("Location", "/api/v1/notes/"+strconv.Itoa(n.ID))
	c.JSON(http.StatusCreated, n)
}

// validationMessage turns a validator tag into the same sentence the other folders use.
func validationMessage(fe validator.FieldError) string {
	switch fe.Tag() {
	case "required":
		return "title is required"
	case "max":
		return "title must be at most 100 characters"
	}
	return "invalid " + strings.ToLower(fe.Field())
}

func parseID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		writeError(c, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return 0, false
	}
	return id, true
}

func (a *API) get(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	n, found := a.store.Get(id)
	if !found {
		writeError(c, http.StatusNotFound, "not_found", "note not found")
		return
	}
	c.JSON(http.StatusOK, n)
}

func (a *API) remove(c *gin.Context) {
	id, ok := parseID(c)
	if !ok {
		return
	}
	if !a.store.Delete(id) {
		writeError(c, http.StatusNotFound, "not_found", "note not found")
		return
	}
	c.Status(http.StatusNoContent)
}

func boom(*gin.Context) {
	panic("something went very wrong")
}

// ---------- middleware ----------

// requestLogger: code before c.Next() runs on the way in, code after it on the way out.
func requestLogger(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		log.Info("request", "method", c.Request.Method, "path", c.Request.URL.Path,
			"status", c.Writer.Status(), "bytes", max(c.Writer.Size(), 0),
			"duration", time.Since(start).Round(time.Microsecond))
	}
}

// recovery is gin's own recovery middleware with a custom handler so the 500 uses our JSON
// envelope. The io.Discard writer silences gin's default stderr stack dump (we log with slog).
func recovery(log *slog.Logger) gin.HandlerFunc {
	return gin.CustomRecoveryWithWriter(io.Discard, func(c *gin.Context, recovered any) {
		log.Error("panic recovered", "panic", recovered, "path", c.Request.URL.Path)
		writeError(c, http.StatusInternalServerError, "internal", "internal server error")
	})
}

// ---------- wiring ----------

func newHandler(store *Store, log *slog.Logger) *gin.Engine {
	api := &API{store: store}

	r := gin.New() // bare engine: no middleware yet
	r.Use(requestLogger(log), recovery(log))

	// Unknown routes / wrong methods go through the same global middleware and our envelope.
	r.HandleMethodNotAllowed = true
	r.NoRoute(func(c *gin.Context) {
		writeError(c, http.StatusNotFound, "not_found", "route not found")
	})
	r.NoMethod(func(c *gin.Context) {
		writeError(c, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	v1 := r.Group("/api/v1")
	v1.GET("/notes", api.list)
	v1.POST("/notes", api.create)
	v1.GET("/notes/:id", api.get)
	v1.DELETE("/notes/:id", api.remove)
	v1.GET("/boom", boom)
	return r
}

func main() {
	serve := flag.Bool("serve", false, "listen on :8080 until Ctrl-C instead of running the demo")
	flag.Parse()

	gin.SetMode(gin.ReleaseMode) // no debug route dump; use gin.TestMode in tests

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == "duration") {
				return slog.Attr{}
			}
			return a
		},
	}))
	h := newHandler(NewStore(), log)

	if *serve {
		if err := run(h); err != nil {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
		return
	}
	demo(h)
}

// run is identical to the stdlib version: *gin.Engine is an http.Handler, so we use a
// normal http.Server (with timeouts and graceful Shutdown) rather than r.Run(":8080"),
// which has neither.
func run(h http.Handler) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv := &http.Server{Addr: ":8080", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Println("listening on :8080 (Ctrl-C to stop)")

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		return err
	}
	if err := <-errCh; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	fmt.Println("shut down cleanly")
	return nil
}

func demo(h http.Handler) {
	call := func(method, path, body string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		out, _ := io.ReadAll(rec.Result().Body)
		fmt.Printf("=> %-6s %-18s %d %s\n", method, path, rec.Code, strings.TrimSpace(string(out)))
	}
	call("POST", "/api/v1/notes", `{"title":"buy milk","body":"2 litres"}`)
	call("POST", "/api/v1/notes", `{"title":""}`)
	call("POST", "/api/v1/notes", `not json`)
	call("GET", "/api/v1/notes", "")
	call("GET", "/api/v1/notes/1", "")
	call("GET", "/api/v1/notes/99", "")
	call("GET", "/api/v1/notes/abc", "")
	call("DELETE", "/api/v1/notes/1", "")
	call("GET", "/api/v1/notes/1", "")
	call("GET", "/api/v1/boom", "")
	call("GET", "/nope", "")
	call("PUT", "/api/v1/notes", "")
}
