// Frameworks 1/4: the notes API with ONLY the standard library (net/http).
//
// This is the reference version. The same API is built again in ../chi, ../gin and
// ../fiber; diff the folders to see exactly what a framework changes (and what it doesn't).
//
// The API (identical in all four):
//
//	GET    /api/v1/notes        list notes
//	POST   /api/v1/notes        create {"title": "...", "body": "..."}; title required, <= 100 chars
//	GET    /api/v1/notes/{id}   fetch one
//	DELETE /api/v1/notes/{id}   delete one (204)
//	GET    /api/v1/boom         panics on purpose, to demonstrate recovery
//
// Errors always use one envelope so clients parse one shape:
//
//	{"error": {"code": "validation_failed", "message": "title is required"}}
//
// stdlib specifics:
//   - Go 1.22+ ServeMux understands methods and wildcards: "GET /notes/{id}" and
//     r.PathValue("id"). That removed the main reason people used to reach for a router.
//   - A handler is func(http.ResponseWriter, *http.Request). Errors are not returned;
//     you write the response and return. (Gin/Chi/Fiber differ here, see README.)
//   - "Route groups" don't exist. We mount a sub-mux under /api/v1/ with http.StripPrefix.
//   - Middleware is func(http.Handler) http.Handler. We wrap the whole mux once.
//   - ServeMux's own 404/405 replies are plain text, not our JSON envelope. Frameworks
//     usually let you customise those; with the stdlib you'd wrap the mux if you care.
//
// Run:
//
//	go run ./06-frameworks/stdlib            # demo requests against an in-memory handler, then exit
//	go run ./06-frameworks/stdlib -serve     # listen on :8080 until Ctrl-C (graceful shutdown)
//	curl -i -X POST localhost:8080/api/v1/notes -d '{"title":"hello"}'
//
// In industry: plenty of Go teams ship exactly this (stdlib + a few small helpers). It has
// no dependencies to upgrade, every library speaks net/http, and new engineers already know it.
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
)

// ---------- JSON helpers ----------

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
	_ = json.NewEncoder(w).Encode(v) // headers are sent; nothing useful to do on error
}

func writeError(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, errorBody{errorDetail{Code: code, Message: msg}})
}

// ---------- handlers ----------

// API holds dependencies; handlers are methods so they can reach the store.
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
	// Cap the body: without this a client can stream gigabytes into our decoder.
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
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

// parseID reads the {id} wildcard. It writes the 400 itself and reports false on failure.
func parseID(w http.ResponseWriter, r *http.Request) (int, bool) {
	id, err := strconv.Atoi(r.PathValue("id"))
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

// statusRecorder remembers the status code, because handlers never tell us.
// Unwrap lets http.ResponseController reach the real writer (Flush, Hijack, ...).
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (s *statusRecorder) WriteHeader(code int) {
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusRecorder) Unwrap() http.ResponseWriter { return s.ResponseWriter }

// requestLogger writes one structured log line per request.
func requestLogger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			log.Info("request", "method", r.Method, "path", r.URL.Path,
				"status", rec.status, "duration", time.Since(start).Round(time.Microsecond))
		})
	}
}

// recoverer turns a panic into a JSON 500 instead of a dropped connection.
// It must sit INSIDE the logger so the logger sees the 500 it writes.
func recoverer(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if v := recover(); v != nil {
					if v == http.ErrAbortHandler {
						panic(v) // intentional abort: let net/http handle it
					}
					log.Error("panic recovered", "panic", v, "path", r.URL.Path)
					writeError(w, http.StatusInternalServerError, "internal", "internal server error")
				}
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- wiring ----------

func newHandler(store *Store, log *slog.Logger) http.Handler {
	api := &API{store: store}

	// The "group": a sub-mux whose patterns do not mention /api/v1.
	v1 := http.NewServeMux()
	v1.HandleFunc("GET /notes", api.list)
	v1.HandleFunc("POST /notes", api.create)
	v1.HandleFunc("GET /notes/{id}", api.get)
	v1.HandleFunc("DELETE /notes/{id}", api.remove)
	v1.HandleFunc("GET /boom", boom)

	root := http.NewServeMux()
	root.Handle("/api/v1/", http.StripPrefix("/api/v1", v1))

	// Outermost first: logger sees everything recoverer writes.
	return requestLogger(log)(recoverer(log)(root))
}

func main() {
	serve := flag.Bool("serve", false, "listen on :8080 until Ctrl-C instead of running the demo")
	flag.Parse()

	// Drop the timestamp so demo output is stable and easy to read.
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

// run serves until SIGINT, then drains in-flight requests (graceful shutdown).
func run(h http.Handler) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	srv := &http.Server{Addr: ":8080", Handler: h, ReadHeaderTimeout: 5 * time.Second}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	fmt.Println("listening on :8080 (Ctrl-C to stop)")

	select {
	case err := <-errCh: // failed to start, e.g. port in use
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

// demo calls the handler directly with httptest: no port, no network, deterministic.
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
}
