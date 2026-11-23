// Lesson 04: middleware
//
// Middleware is a function that takes a Handler and returns a Handler:
//
//	type Middleware func(http.Handler) http.Handler
//
// The returned handler does something before and/or after calling next.ServeHTTP,
// or decides not to call next at all (auth failure, rate limit, CORS preflight).
// Because the input and output are both http.Handler, middlewares compose like
// Russian dolls, and a ServeMux is just another Handler in the middle.
//
// ORDER MATTERS. chain(h, A, B, C) means A(B(C(h))): A runs first on the way in
// and last on the way out. Our order and why:
//
//  1. requestID  - outermost, so every later log line and error can carry the id
//  2. accessLog  - outside recoverer, so it sees the 500 that recoverer writes,
//     and measures the whole request time
//  3. recoverer  - outside everything that can panic (handlers, other middleware)
//  4. cors       - before auth/CSRF: a preflight OPTIONS carries no credentials and
//     must be answered before anything can reject it with 401/403
//  5. csrf       - http.CrossOriginProtection (Go 1.25), rejects cross-site writes
//  6. (per route) auth, timeout - only where needed
//     Swap accessLog and recoverer and a panic would never be logged as a 500.
//
// Pieces in this file:
//   - requestID: reuse a sane incoming X-Request-ID or mint a UUIDv7; stash it in the
//     context under a private typed key (never a bare string: other packages could
//     collide); echo it in the response so clients can quote it in bug reports.
//   - accessLog: structured log/slog line per request with method, path, status,
//     duration, bytes. Handlers never tell us the status, so we wrap the
//     ResponseWriter and record it. The wrapper MUST implement Unwrap() so
//     http.ResponseController can still reach Flush/Hijack/SetWriteDeadline on the
//     real writer (a wrapper without Unwrap silently breaks streaming).
//   - recoverer: a panic in a handler would otherwise kill only that request's
//     goroutine (net/http recovers, logs, and closes the connection). We turn it into a
//     clean JSON 500 and log the stack. Re-panic http.ErrAbortHandler (intentional abort).
//   - cors: browsers enforce the same-origin policy. For cross-origin JS calls they
//     first send a "preflight" (OPTIONS with Access-Control-Request-Method) asking
//     "may I?". We answer with Allow-Origin/Methods/Headers and 204. For real requests
//     we add Allow-Origin. Allow-list exact origins; never reflect any Origin back.
//     CORS is a browser rule, not security for your API: curl ignores it.
//   - csrf: Cross-Site Request Forgery tricks a logged-in browser into sending a state
//     changing request. CrossOriginProtection rejects non-safe (not GET/HEAD/OPTIONS)
//     requests whose Sec-Fetch-Site/Origin say "cross-site", with no tokens needed.
//     Requests without those headers (curl, server to server) pass.
//   - http.TimeoutHandler: gives one route a deadline and answers 503 when exceeded. It
//     buffers the response and does not support Flush/Hijack, so use it per route,
//     not around streaming endpoints. The handler must watch r.Context() to stop.
//   - requireBearer: checks "Authorization: Bearer <token>" in constant time, applied
//     only to the /admin/ subtree.
//
// Run with -serve:
//
//	go run ./04-web-stdlib/04middleware -serve
//	curl -i localhost:8080/hello -H 'X-Request-ID: abc-123'
//	curl -i localhost:8080/panic
//	curl -i localhost:8080/slow
//	curl -i localhost:8080/admin/stats                              # 401
//	curl -i localhost:8080/admin/stats -H 'Authorization: Bearer s3cret'
//	curl -i -X OPTIONS localhost:8080/orders -H 'Origin: https://app.example.com' \
//	     -H 'Access-Control-Request-Method: POST'                   # preflight
//	curl -i -X POST localhost:8080/orders -H 'Origin: https://evil.example' \
//	     -H 'Sec-Fetch-Site: cross-site'                            # 403 from CSRF protection
//
// In industry: the same chain exists in every service (request id, logging/metrics,
// recovery, auth, rate limiting, tracing via OpenTelemetry's otelhttp). Teams put it in one
// shared package so all services behave alike. Keep middleware thin and stateless;
// business rules belong in handlers/services.
package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"strings"
	"time"
	"uuid"
)

type Middleware func(http.Handler) http.Handler

// chain applies middlewares so that the FIRST one listed is the OUTERMOST.
func chain(h http.Handler, mws ...Middleware) http.Handler {
	for _, mw := range slices.Backward(mws) { // wrap from the inside out
		h = mw(h)
	}
	return h
}

// ---------- request id ----------

type ctxKey int // unexported type => no other package can read or collide with our keys

const requestIDKey ctxKey = iota

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}

// validRequestID guards against log injection and absurd header sizes.
func validRequestID(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for _, c := range s {
		ok := c == '-' || c == '_' || (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z')
		if !ok {
			return false
		}
	}
	return true
}

func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.NewV7().String()
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), requestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx)) // requests are immutable-ish: derive a new one
	})
}

// ---------- access log ----------

// responseWriter records what the handler did. Embedding http.ResponseWriter forwards
// everything we do not override.
type responseWriter struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (w *responseWriter) WriteHeader(code int) {
	if w.status == 0 { // only the first call counts, like net/http
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK // Write without WriteHeader means an implicit 200
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap lets http.NewResponseController find the underlying writer's optional
// capabilities (Flush, Hijack, SetReadDeadline, SetWriteDeadline).
func (w *responseWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func accessLog(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w}
			next.ServeHTTP(rw, r)
			if rw.status == 0 {
				rw.status = http.StatusOK // handler wrote nothing at all
			}
			logger.LogAttrs(r.Context(), slog.LevelInfo, "request",
				slog.String("request_id", requestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Duration("duration", time.Since(start).Round(time.Millisecond)),
				slog.Int("bytes", rw.bytes),
			)
		})
	}
}

// ---------- panic recovery ----------

func writeJSONError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"code":%q,"message":%q}}`+"\n", code, msg)
}

func recoverer(logger *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if v == http.ErrAbortHandler {
					panic(v) // deliberate abort: let net/http handle it silently
				}
				logger.ErrorContext(r.Context(), "panic recovered",
					"request_id", requestIDFrom(r.Context()),
					"panic", fmt.Sprint(v),
					"stack", string(debug.Stack()))
				// If the handler already wrote part of the response this header call is
				// a no-op (logged as superfluous), which is another reason to buffer or
				// write responses at the end of handlers.
				writeJSONError(w, http.StatusInternalServerError, "internal", "internal server error")
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- CORS ----------

func cors(allowedOrigins ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin != "" && slices.Contains(allowedOrigins, origin) {
				h := w.Header()
				h.Set("Access-Control-Allow-Origin", origin) // exact match from the allow-list
				h.Add("Vary", "Origin")                      // response differs per Origin: caches must know

				isPreflight := r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != ""
				if isPreflight {
					h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE")
					h.Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Request-ID")
					h.Set("Access-Control-Max-Age", "600") // browser may cache the answer 10 min
					w.WriteHeader(http.StatusNoContent)    // answer here; do not call next
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- auth ----------

func requireBearer(token string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			// ConstantTimeCompare avoids leaking how many leading bytes matched via timing.
			if !ok || subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
				w.Header().Set("WWW-Authenticate", `Bearer realm="admin"`)
				writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid bearer token")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// ---------- app ----------

type appConfig struct {
	logger         *slog.Logger
	adminToken     string
	allowedOrigins []string
	slowTimeout    time.Duration
}

func newHandler(cfg appConfig) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /hello", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello (request id %s)\n", requestIDFrom(r.Context()))
	})
	mux.HandleFunc("GET /panic", func(w http.ResponseWriter, r *http.Request) {
		var m map[string]int
		m["boom"] = 1 // assignment to nil map: a classic runtime panic
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, "order created\n")
	})

	// Streaming: the Flush works only because responseWriter implements Unwrap.
	mux.HandleFunc("GET /stream", func(w http.ResponseWriter, r *http.Request) {
		rc := http.NewResponseController(w)
		for i := 1; i <= 2; i++ {
			fmt.Fprintf(w, "chunk %d\n", i)
			if err := rc.Flush(); err != nil {
				cfg.logger.Error("flush failed", "err", err) // ErrNotSupported without Unwrap
				return
			}
		}
	})

	// Per-route timeout. The handler watches the request context, which TimeoutHandler cancels.
	slow := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second): // pretend this is a slow database call
			io.WriteString(w, "finally done\n")
		case <-r.Context().Done():
		}
	})
	mux.Handle("GET /slow", http.TimeoutHandler(slow, cfg.slowTimeout, "request took too long\n"))

	// Auth only on the admin subtree.
	admin := http.NewServeMux()
	admin.HandleFunc("GET /admin/stats", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "secret stats\n")
	})
	mux.Handle("/admin/", requireBearer(cfg.adminToken)(admin))

	// Global middleware. csrf also needs to know which cross-origin callers we trust.
	csrf := http.NewCrossOriginProtection()
	for _, o := range cfg.allowedOrigins {
		if err := csrf.AddTrustedOrigin(o); err != nil {
			panic(err) // programmer error: malformed origin constant
		}
	}
	return chain(mux,
		requestID,
		accessLog(cfg.logger),
		recoverer(cfg.logger),
		cors(cfg.allowedOrigins...),
		csrf.Handler,
	)
}

func main() {
	serve := flag.Bool("serve", false, "run a real server on :8080 until Ctrl-C")
	flag.Parse()

	if *serve {
		cfg := appConfig{
			logger:         slog.New(slog.NewTextHandler(os.Stderr, nil)),
			adminToken:     "s3cret",
			allowedOrigins: []string{"https://app.example.com"},
			slowTimeout:    2 * time.Second,
		}
		runForever(&http.Server{Addr: ":8080", Handler: newHandler(cfg), ReadHeaderTimeout: 5 * time.Second})
		return
	}
	demo()
}

func demo() {
	// Print logs on stdout without the timestamp, and drop the long stack trace for readability.
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == "stack") {
				return slog.Attr{} // an empty Attr removes the field
			}
			return a
		},
	}))
	ts := httptest.NewServer(newHandler(appConfig{
		logger:         logger,
		adminToken:     "s3cret",
		allowedOrigins: []string{"https://app.example.com"},
		slowTimeout:    100 * time.Millisecond,
	}))
	defer ts.Close()

	type call struct {
		label, method, path string
		headers             map[string]string
	}
	calls := []call{
		{"generated request id", "GET", "/hello", nil},
		{"client supplied request id is reused", "GET", "/hello", map[string]string{"X-Request-ID": "abc-123"}},
		{"panic becomes JSON 500", "GET", "/panic", nil},
		{"streaming works through the wrapper", "GET", "/stream", nil},
		{"TimeoutHandler gives 503", "GET", "/slow", nil},
		{"admin without token", "GET", "/admin/stats", nil},
		{"admin with token", "GET", "/admin/stats", map[string]string{"Authorization": "Bearer s3cret"}},
		{"CORS preflight", "OPTIONS", "/orders", map[string]string{
			"Origin": "https://app.example.com", "Access-Control-Request-Method": "POST"}},
		{"cross-site POST blocked by CSRF protection", "POST", "/orders", map[string]string{
			"Origin": "https://evil.example", "Sec-Fetch-Site": "cross-site"}},
		{"trusted cross-origin POST allowed", "POST", "/orders", map[string]string{
			"Origin": "https://app.example.com", "Sec-Fetch-Site": "cross-site"}},
		{"non-browser POST (no Origin) allowed", "POST", "/orders", nil},
	}
	for _, c := range calls {
		req, _ := http.NewRequest(c.method, ts.URL+c.path, nil)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		fmt.Printf("\n--- %s\n", c.label)
		resp, err := ts.Client().Do(req)
		if err != nil {
			log.Fatal(err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		fmt.Printf("=> %s | X-Request-ID=%q", resp.Status, resp.Header.Get("X-Request-ID"))
		if v := resp.Header.Get("Access-Control-Allow-Origin"); v != "" {
			fmt.Printf(" | Allow-Origin=%s", v)
		}
		if v := resp.Header.Get("Access-Control-Allow-Methods"); v != "" {
			fmt.Printf(" Allow-Methods=%q", v)
		}
		fmt.Printf("\n%s", body)
	}
}

func runForever(srv *http.Server) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sc)
	}()
	log.Printf("listening on %s (Ctrl-C to stop)", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
