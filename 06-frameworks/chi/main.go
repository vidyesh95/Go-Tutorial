// Frameworks 2/4: the notes API with chi (github.com/go-chi/chi/v5).
//
// Compare with ../stdlib: handlers are IDENTICAL net/http handlers. chi only replaces the
// router, and adds nicer grouping and a middleware toolbox.
//
// Why chi is the usual "framework" pick in Go:
//   - Handlers are plain http.Handler / http.HandlerFunc (func(w, r)). chi has no custom
//     context type, so ALL stdlib-compatible middleware and libraries work unchanged:
//     http.TimeoutHandler, http.MaxBytesHandler, otelhttp, rs/cors, httprate, gorilla/...
//     We use http.MaxBytesHandler below to show it: it is a stdlib function, not chi's.
//   - chi is tiny, has no third-party dependencies, and its router is a radix tree.
//   - r.Route("/api/v1", func(r chi.Router) {...}) gives real route groups; r.Use adds
//     middleware to just that group; r.With(mw).Get(...) adds it to a single route.
//   - Path params: chi.URLParam(r, "id"). (Since chi 5.1 r.PathValue("id") works too,
//     because chi fills the stdlib wildcards.)
//   - Hooks for r.NotFound and r.MethodNotAllowed let us keep the JSON error envelope.
//   - chi/middleware ships RequestID, RealIP, Recoverer, Timeout, Compress, Throttle...
//     We use RequestID and NewWrapResponseWriter (which does what our hand written
//     statusRecorder did in ../stdlib). Its Recoverer writes a plain-text 500, so we keep
//     our own recoverer to return the JSON envelope; in a real service you'd pick one.
//
// Differences you can see by diffing with ../stdlib:
//   - the route table (newHandler) is shorter and groups are first class
//   - parseID uses chi.URLParam
//   - the status recorder is gone (chi's WrapResponseWriter)
//
// Run:
//
//	go run ./06-frameworks/chi            # demo requests in memory, then exit
//	go run ./06-frameworks/chi -serve     # listen on :8080 until Ctrl-C
//
// In industry: chi is the most common choice for teams that want a router but also want to
// stay on net/http. Many services started on Gin or Echo and moved to chi/stdlib later for
// exactly that compatibility.
package main

import (
	"context"
	"encoding/json"
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

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// ---------- JSON helpers (same as stdlib) ----------

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{errorDetail{Code: code, Message: msg}})
}

// ---------- handlers (same shape as stdlib) ----------

type API struct {
	store *Store
}

func (a *API) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, a.store.List())
}

type createRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (a *API) create(w http.ResponseWriter, r *http.Request) {
	var req createRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "bad_json", "request body must be valid JSON")
		return
	}
	title := strings.TrimSpace(req.Title)
	if msg := validateTitle(title); msg != "" {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", msg)
		return
	}
	n := a.store.Create(title, req.Body)
	w.Header().Set("Location", "/api/v1/notes/"+strconv.Itoa(n.ID))
	writeJSON(w, http.StatusCreated, n)
}

func parseID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(chi.URLParam(r, "id")) // the only line that differs from stdlib
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_id", "id must be an integer")
		return 0, false
	}
	return id, true
}

func (a *API) get(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	n, found := a.store.Get(id)
	if !found {
		writeError(w, http.StatusNotFound, "not_found", "note not found")
		return
	}
	writeJSON(w, http.StatusOK, n)
}

func (a *API) remove(w http.ResponseWriter, r *http.Request) {
	id, ok := parseID(w, r)
	if !ok {
		return
	}
	if !a.store.Delete(id) {
		writeError(w, http.StatusNotFound, "not_found", "note not found")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func boom(http.ResponseWriter, *http.Request) {
	panic("something went very wrong")
}

// ---------- middleware ----------

// requestLogger: one slog line per request. middleware.NewWrapResponseWriter records the
// status and byte count for us; middleware.GetReqID reads the id set by middleware.RequestID.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
			next.ServeHTTP(ww, r)
			status := ww.Status()
			if status == 0 { // handler never wrote anything: net/http sends 200
				status = http.StatusOK
			}
			log.Info("request", "id", middleware.GetReqID(r.Context()), "method", r.Method,
				"path", r.URL.Path, "status", status, "bytes", ww.BytesWritten(),
				"duration", time.Since(start).Round(time.Microsecond))
		})
	}
}

// recoverer: JSON envelope version of middleware.Recoverer.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v)
					}
					log.Error("panic recovered", "panic", v, "path", r.URL.Path)
					writeError(w, http.StatusInternalServerError, "internal", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// limitBody adapts a plain stdlib wrapper to chi's r.Use signature. Any
// func(http.Handler) http.Handler is a valid chi middleware, so this is all it takes.
func limitBody(next http.Handler) http.Handler {
	return http.MaxBytesHandler(next, 1<<20)
}

// ---------- wiring ----------

func newHandler(store *Store, log *slog.Logger) http.Handler {
	api := &API{store: store}
	r := chi.NewRouter()

	// Global middleware, outermost first.
	r.Use(middleware.RequestID, requestLogger(log), recoverer(log), limitBody)

	// Keep the error envelope for unknown routes and wrong methods too.
	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	// The route group.
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/notes", api.list)
		r.Post("/notes", api.create)
		r.Get("/notes/{id}", api.get)
		r.Delete("/notes/{id}", api.remove)
		r.Get("/boom", boom)
	})
	return r
}

func main() {
	serve := flag.Bool("serve", false, "listen on :8080 until Ctrl-C instead of running the demo")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			// stable demo output: drop time, duration and the random request id
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == "duration" || a.Key == "id") {
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
