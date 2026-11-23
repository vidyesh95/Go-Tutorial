package httpapi

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"
	"uuid"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// A middleware wraps a handler to add behaviour before/after it (logging, auth, ...).
type middleware func(http.Handler) http.Handler

// chain applies middlewares so that the FIRST one listed is the OUTERMOST (runs first on the way
// in, last on the way out).
func chain(h http.Handler, mws ...middleware) http.Handler {
	for _, mw := range slices.Backward(mws) {
		h = mw(h)
	}
	return h
}

// ctxKey is an unexported type so no other package can collide with (or forge) our context keys.
type ctxKey int

const (
	ctxRequestID ctxKey = iota
	ctxUserID
)

func requestIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxRequestID).(string)
	return id
}

// userIDFrom returns the authenticated user's id. It is only meaningful inside handlers wrapped
// by requireAuth.
func userIDFrom(ctx context.Context) string {
	id, _ := ctx.Value(ctxUserID).(string)
	return id
}

// --- statusWriter ---------------------------------------------------------------------------

// statusWriter records the status code and body size so middleware can log and measure them
// after the handler is done. http.ResponseWriter offers no way to read them back.
type statusWriter struct {
	http.ResponseWriter
	status int
	bytes  int
	wrote  bool
}

func (w *statusWriter) WriteHeader(code int) {
	if !w.wrote {
		w.status, w.wrote = code, true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.wrote {
		w.WriteHeader(http.StatusOK) // net/http sends an implicit 200 on first Write
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// Unwrap lets http.ResponseController reach the real writer (Flush, SetWriteDeadline, ...)
// through our wrapper.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// asStatusWriter reuses an existing wrapper so that logging and metrics share one.
func asStatusWriter(w http.ResponseWriter) *statusWriter {
	if sw, ok := w.(*statusWriter); ok {
		return sw
	}
	return &statusWriter{ResponseWriter: w, status: http.StatusOK}
}

// --- Request ID -------------------------------------------------------------------------------

// requestID gives every request an id, returns it in X-Request-ID and stores it in the context,
// so one id ties together the client's report, the access log line and any error log.
//
// An id supplied by the client (or an upstream proxy) is kept only if it looks sane; otherwise a
// client could inject arbitrary text (log forging, huge strings) into our logs.
func requestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !validRequestID(id) {
			id = uuid.NewV7().String()
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxRequestID, id)))
	})
}

func validRequestID(id string) bool {
	if id == "" || len(id) > 64 {
		return false
	}
	for _, c := range id {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}

// --- Access log -------------------------------------------------------------------------------

// routeOf returns the matched mux pattern without its method ("GET /v1/tasks/{id}" ->
// "/v1/tasks/{id}"), or "unmatched". Pattern is filled in by ServeMux while routing, so it is
// only readable AFTER next.ServeHTTP returns, and only on the same *http.Request the mux
// received. That is why no middleware between here and the mux may replace r (r.WithContext
// makes a copy): the outer layers would then never see the pattern.
//
// Using the pattern instead of r.URL.Path is essential for metrics: "/v1/tasks/0192..." has
// unbounded distinct values and would create a new time series per task (cardinality explosion).
func routeOf(r *http.Request) string {
	if r.Pattern == "" {
		return "unmatched"
	}
	if _, route, ok := strings.Cut(r.Pattern, " "); ok {
		return route
	}
	return r.Pattern
}

// accessLog writes one structured line per request. It logs the path but never the query string
// or headers: Authorization headers and tokens must never reach logs.
func accessLog(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			sw := asStatusWriter(w)
			next.ServeHTTP(sw, r)

			level := slog.LevelInfo
			route := routeOf(r)
			switch {
			case sw.status >= 500:
				level = slog.LevelError
			case route == "/healthz" || route == "/readyz" || route == "/metrics":
				level = slog.LevelDebug // probes and scrapes every few seconds would drown real traffic
			}
			log.LogAttrs(r.Context(), level, "http request",
				slog.String("request_id", requestIDFrom(r.Context())),
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.String("route", route),
				slog.Int("status", sw.status),
				slog.Int("bytes", sw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("remote_ip", clientIP(r)),
			)
		})
	}
}

// --- Panic recovery -----------------------------------------------------------------------------

// recoverer turns a panic in a handler into a logged 500. Without it net/http would recover
// anyway, but it would close the connection with no response and log through the global logger;
// this way clients get a proper error and the stack trace lands in our structured log.
func recoverer(log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				rec := recover()
				if rec == nil {
					return
				}
				if err, ok := rec.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(rec) // deliberate abort signal; let net/http handle it
				}
				log.ErrorContext(r.Context(), "panic recovered",
					"panic", rec, "stack", string(debug.Stack()),
					"request_id", requestIDFrom(r.Context()))
				writeError(w, r, log, errors.New("panic"))
			}()
			next.ServeHTTP(w, r)
		})
	}
}

// --- CORS ---------------------------------------------------------------------------------------

// cors lets browser apps from the listed origins call the API. CORS is enforced by the BROWSER;
// it does not protect the API from curl or other servers (authentication does that).
//
// We use bearer tokens in the Authorization header rather than cookies, so we never send
// Access-Control-Allow-Credentials and a cross-site page cannot ride on a victim's ambient
// credentials. An empty allow-list disables CORS entirely (the safe default).
func cors(allowed []string) middleware {
	return func(next http.Handler) http.Handler {
		if len(allowed) == 0 {
			return next
		}
		anyOrigin := slices.Contains(allowed, "*")
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" || !(anyOrigin || slices.Contains(allowed, origin)) {
				next.ServeHTTP(w, r)
				return
			}
			h := w.Header()
			h.Add("Vary", "Origin") // responses differ per Origin; caches must know
			if anyOrigin {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
			}
			h.Set("Access-Control-Expose-Headers", "X-Request-ID, Location")

			// A preflight is an OPTIONS request carrying Access-Control-Request-Method.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Set("Access-Control-Allow-Methods", "GET, POST, PATCH, DELETE, OPTIONS")
				h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
				h.Set("Access-Control-Max-Age", "600")
				w.WriteHeader(http.StatusNoContent)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// --- Authentication -------------------------------------------------------------------------------

// TokenVerifier validates a bearer token. *auth.Issuer satisfies it.
type TokenVerifier interface {
	Parse(token string) (auth.Claims, error)
}

// requireAuth rejects requests without a valid "Authorization: Bearer <jwt>" and otherwise puts
// the user id in the request context. It is applied per route (not globally) so public routes
// like login stay open.
//
// The token is stateless: we trust the signature and expiry without a database lookup. The
// tradeoff is that a token stays valid until it expires even if the user is deleted or the
// password changes. Short TTLs plus refresh tokens (README: next steps) shrink that window.
func requireAuth(tokens TokenVerifier, log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			scheme, token, found := strings.Cut(r.Header.Get("Authorization"), " ")
			if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
				writeError(w, r, log, domain.ErrUnauthorized)
				return
			}
			claims, err := tokens.Parse(strings.TrimSpace(token))
			if err != nil {
				// Log the reason at debug (useful when diagnosing "why am I logged out?"), but
				// the client only ever sees a generic 401. The token itself is never logged.
				log.DebugContext(r.Context(), "token rejected", "reason", err, "request_id", requestIDFrom(r.Context()))
				writeError(w, r, log, domain.ErrUnauthorized)
				return
			}
			ctx := context.WithValue(r.Context(), ctxUserID, claims.UserID)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// --- Metrics ----------------------------------------------------------------------------------------

// metrics holds the Prometheus collectors. They are created per handler, against a registry the
// caller owns, rather than the package-global default registry: no global state, and tests can
// build many independent handlers without "duplicate registration" panics.
type metrics struct {
	requests *prometheus.CounterVec
	duration *prometheus.HistogramVec
}

func newMetrics(reg prometheus.Registerer) *metrics {
	m := &metrics{
		requests: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP requests processed, by method, route pattern and status code.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "HTTP request latency in seconds, by method and route pattern.",
			Buckets: prometheus.DefBuckets,
		}, []string{"method", "route"}),
	}
	reg.MustRegister(m.requests, m.duration)
	return m
}

// knownMethods bounds the "method" label: clients may send any string as a method, and each
// distinct value would otherwise become its own time series.
var knownMethods = []string{
	http.MethodGet, http.MethodHead, http.MethodPost, http.MethodPut,
	http.MethodPatch, http.MethodDelete, http.MethodOptions,
}

func (m *metrics) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := asStatusWriter(w)
		next.ServeHTTP(sw, r)

		method := r.Method
		if !slices.Contains(knownMethods, method) {
			method = "OTHER"
		}
		route := routeOf(r) // read AFTER the mux has run; see routeOf
		m.requests.WithLabelValues(method, route, strconv.Itoa(sw.status)).Inc()
		m.duration.WithLabelValues(method, route).Observe(time.Since(start).Seconds())
	})
}
