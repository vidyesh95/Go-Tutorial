// Package httpapi is the HTTP transport layer: routing, middleware, JSON encoding and the
// mapping from domain errors to status codes. It contains NO business rules; handlers decode a
// request, call a service, and encode the result.
//
// Request flow (outermost middleware first):
//
//	requestID -> accessLog -> metrics -> recoverer -> cors -> rateLimit -> ServeMux
//	                                                                          |
//	                       (per route) [csrf, strict rate limit] requireAuth -> handler -> service -> store
//
// requireAuth accepts a Bearer JWT or a session cookie; see its documentation.
//
// Order matters:
//   - requestID is outermost so every later log line carries the id.
//   - accessLog and metrics sit outside recoverer so they see the 500 that a panic becomes.
//   - rateLimit runs before routing and auth, so floods are rejected cheaply, before any token
//     parsing or database work.
//   - Nothing between metrics and the mux may replace the *http.Request (see routeOf).
package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/service"
)

// Pinger is the slice of *sql.DB the readiness probe needs.
type Pinger interface {
	PingContext(ctx context.Context) error
}

// Deps is everything the HTTP layer needs, passed explicitly (no globals, no init()).
type Deps struct {
	Logger   *slog.Logger
	Auth     *service.AuthService
	Projects *service.ProjectService
	Tasks    *service.TaskService
	Tokens   TokenVerifier
	DB       Pinger

	// SessionAuth (required) powers the cookie-session mode and the /v1/session-auth/* endpoints.
	SessionAuth *service.SessionAuthService
	// GitHub enables "Sign in with GitHub" when non-nil; the OAuth routes are 404 otherwise.
	GitHub *auth.GitHub
	// CookieSecure sets the Secure attribute on cookies. true in production; false only for
	// plain-http local development.
	CookieSecure bool

	// Draining flips to true when shutdown begins; /readyz then returns 503 so the load balancer
	// stops sending new traffic while in-flight requests finish. May be nil.
	Draining *atomic.Bool

	RateRPS     float64
	RateBurst   int
	CORSOrigins []string

	// AuthRateRPS/AuthRateBurst are the (much stricter) per-IP limit for credential endpoints:
	// sign-in, sign-up, password reset, verification mail. Zero falls back to RateRPS/RateBurst.
	AuthRateRPS   float64
	AuthRateBurst int

	// CleanupInterval is how often expired sessions and tokens are deleted (default 1 hour).
	CleanupInterval time.Duration
}

// server carries the dependencies shared by all handlers.
type server struct {
	Deps
	cookies cookieConfig
}

// NewHandler builds the complete HTTP handler. ctx bounds background goroutines started here
// (the rate limiter's janitor): cancel it on shutdown.
func NewHandler(ctx context.Context, d Deps) http.Handler {
	s := &server{Deps: d, cookies: cookieConfig{Name: sessionCookieName, Secure: d.CookieSecure, TTL: d.SessionAuth.TTL()}}

	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector(), collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	m := newMetrics(reg)

	// CSRF protection for cookie authentication (see requireAuth). Browsers calling from a
	// different origin, such as a SPA on http://localhost:3000, must be listed in the CORS
	// allow-list, and those explicit origins are trusted here as well.
	cop := http.NewCrossOriginProtection()
	for _, origin := range d.CORSOrigins {
		if origin == "*" {
			continue // a wildcard must never become a CSRF trust list
		}
		if err := cop.AddTrustedOrigin(origin); err != nil {
			d.Logger.Warn("ignoring CORS origin for CSRF trust", "origin", origin, "error", err)
		}
	}
	authed := requireAuth(d.Tokens, d.SessionAuth, s.cookies, cop, d.Logger)
	csrf := csrfProtect(cop, d.Logger)

	authRPS, authBurst := d.AuthRateRPS, d.AuthRateBurst
	if authRPS <= 0 || authBurst < 1 {
		authRPS, authBurst = d.RateRPS, d.RateBurst
	}
	// strict = a second, tighter per-IP bucket, applied on top of the global one. Password
	// guessing is the attack these endpoints invite; 0.5 req/s (30/min) with a burst of 10 is
	// plenty for humans and makes online guessing impractical. (Per IP only: an attacker with many
	// IPs can still spread guesses, so also consider per-ACCOUNT lockout/backoff; README, next steps.)
	strict := rateLimit(newIPLimiter(ctx, authRPS, authBurst), d.Logger)

	// Go 1.22+ ServeMux patterns: "METHOD /path/{wildcard}". The method is part of the pattern,
	// so a wrong method gets an automatic 405 with an Allow header, and r.PathValue reads
	// wildcards. No third-party router needed.
	mux := http.NewServeMux()

	// --- Mode 1: stateless JWT (mobile apps, services, scripts) -------------------------------
	mux.Handle("POST /v1/auth/signup", strict(s.handle(s.signup)))
	mux.Handle("POST /v1/auth/login", strict(s.handle(s.login)))

	// --- Mode 2: database sessions in an HttpOnly cookie (web apps), Better Auth-style -----
	// Unsafe methods pass through csrf (CrossOriginProtection); the ones behind `authed` get
	// that check from requireAuth when they arrive with a cookie.
	mux.Handle("POST /v1/session-auth/sign-up/email", chain(s.handle(s.signUpEmail), csrf, strict))
	mux.Handle("POST /v1/session-auth/sign-in/email", chain(s.handle(s.signInEmail), csrf, strict))
	mux.Handle("POST /v1/session-auth/sign-out", csrf(s.handle(s.signOut)))
	mux.Handle("GET /v1/session-auth/get-session", authed(s.handle(s.getSession)))
	mux.Handle("GET /v1/session-auth/list-sessions", authed(s.handle(s.listSessions)))
	mux.Handle("POST /v1/session-auth/revoke-session", authed(s.handle(s.revokeSession)))
	mux.Handle("POST /v1/session-auth/revoke-other-sessions", authed(s.handle(s.revokeOtherSessions)))
	mux.Handle("POST /v1/session-auth/send-verification-email", strict(authed(s.handle(s.sendVerificationEmail))))
	mux.Handle("GET /v1/session-auth/verify-email", strict(s.handle(s.verifyEmail)))
	mux.Handle("POST /v1/session-auth/forget-password", chain(s.handle(s.forgetPassword), csrf, strict))
	mux.Handle("POST /v1/session-auth/reset-password", chain(s.handle(s.resetPassword), csrf, strict))
	mux.Handle("GET /v1/session-auth/oauth/github", s.handle(s.githubStart))
	mux.Handle("GET /v1/session-auth/oauth/github/callback", s.handle(s.githubCallback))

	// Works with either mode.
	mux.Handle("GET /v1/me", authed(s.handle(s.me)))

	mux.Handle("GET /v1/projects", authed(s.handle(s.listProjects)))
	mux.Handle("POST /v1/projects", authed(s.handle(s.createProject)))
	mux.Handle("GET /v1/projects/{id}", authed(s.handle(s.getProject)))
	mux.Handle("PATCH /v1/projects/{id}", authed(s.handle(s.renameProject)))
	mux.Handle("DELETE /v1/projects/{id}", authed(s.handle(s.deleteProject)))

	mux.Handle("GET /v1/projects/{id}/tasks", authed(s.handle(s.listTasks)))
	mux.Handle("POST /v1/projects/{id}/tasks", authed(s.handle(s.createTask)))
	mux.Handle("GET /v1/tasks/{id}", authed(s.handle(s.getTask)))
	mux.Handle("PATCH /v1/tasks/{id}", authed(s.handle(s.updateTask)))
	mux.Handle("DELETE /v1/tasks/{id}", authed(s.handle(s.deleteTask)))

	// Operational endpoints. Unauthenticated by design: probes and scrapers run inside the
	// cluster. In production restrict /metrics at the network level (separate port or ingress
	// rules) rather than exposing it to the internet.
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.Handle("GET /metrics", promhttp.HandlerFor(reg, promhttp.HandlerOpts{}))

	// Housekeeping goroutine: stops when ctx is cancelled.
	every := d.CleanupInterval
	if every <= 0 {
		every = time.Hour
	}
	go d.SessionAuth.RunCleanup(ctx, every)

	limiter := newIPLimiter(ctx, d.RateRPS, d.RateBurst)
	return chain(mux,
		requestID,
		accessLog(d.Logger),
		m.middleware,
		recoverer(d.Logger),
		cors(d.CORSOrigins),
		rateLimit(limiter, d.Logger),
	)
}

// handlerFunc is an http.HandlerFunc that returns an error. Returning errors (instead of
// writing them at every failure site) keeps handlers short and funnels all error rendering
// through mapError.
type handlerFunc func(w http.ResponseWriter, r *http.Request) error

func (s *server) handle(h handlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			writeError(w, r, s.Logger, err)
		}
	})
}

// --- Health ---------------------------------------------------------------------------------------

// healthz is liveness: "the process is up and serving". It must not depend on the database,
// otherwise a database outage would make the orchestrator restart healthy app instances.
func (s *server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz is readiness: "send me traffic". Not ready while draining or when the database is
// unreachable, so the load balancer routes around this instance without killing it.
func (s *server) readyz(w http.ResponseWriter, r *http.Request) {
	if s.Draining != nil && s.Draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "shutting_down"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), time.Second)
	defer cancel()
	if err := s.DB.PingContext(ctx); err != nil {
		s.Logger.WarnContext(r.Context(), "readiness check failed", "error", err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "database_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
