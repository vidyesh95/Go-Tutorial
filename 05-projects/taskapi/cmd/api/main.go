// Command api runs the taskapi HTTP server.
//
// main.go is wiring only: read config, build the dependency graph from the bottom up
// (database -> store -> services -> HTTP handler), start the server, and shut it down cleanly.
// No business logic lives here, and nothing is global: every dependency is created in run and
// handed to the next layer explicitly, which is what makes each layer testable in isolation.
//
//	TASKAPI_JWT_SECRET=$(openssl rand -base64 48) go run ./05-projects/taskapi/cmd/api
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/config"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/httpapi"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/mailer"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/service"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/store"
)

func main() {
	// SIGINT is Ctrl-C; SIGTERM is what Docker, Kubernetes and systemd send to stop a process.
	// NotifyContext turns the first of them into a cancelled context.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)

	err := run(ctx, os.Args[1:], os.Getenv, os.Stdout)
	stop()
	if err != nil {
		fmt.Fprintln(os.Stderr, "taskapi:", err)
		os.Exit(1)
	}
}

// run is the real main. Taking its inputs (context, args, environment, output) as parameters
// instead of reading globals lets tests start the whole application in-process, on a temporary
// database and port, and stop it by cancelling the context.
func run(ctx context.Context, args []string, getenv func(string) string, stdout io.Writer) error {
	fs := flag.NewFlagSet("taskapi", flag.ContinueOnError)
	fs.SetOutput(stdout)
	migrateOnly := fs.Bool("migrate-only", false, "apply database migrations and exit (for a deploy step)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log := newLogger(cfg, stdout)
	log.Info("starting", "config", cfg) // Config redacts the secrets via slog.LogValuer

	db, err := store.Open(ctx, cfg.DBPath)
	if err != nil {
		return err
	}
	defer db.Close()
	if err := store.Migrate(ctx, db); err != nil {
		return err
	}
	if *migrateOnly {
		log.Info("migrations applied")
		return nil
	}

	handlerCtx, stopHandler := context.WithCancel(context.WithoutCancel(ctx))
	defer stopHandler()
	draining := &atomic.Bool{}
	handler, err := buildHandler(handlerCtx, cfg, log, db, draining)
	if err != nil {
		return err
	}

	srv := &http.Server{
		Addr:    cfg.Addr,
		Handler: handler,
		// Timeouts are not optional in production. Without them a client that connects and then
		// sends nothing (slowloris), or reads its response one byte a minute, holds a goroutine
		// and file descriptor forever.
		ReadHeaderTimeout: 5 * time.Second,  // time to send the request headers
		ReadTimeout:       15 * time.Second, // time to send the whole request incl. body
		WriteTimeout:      30 * time.Second, // time to write the response
		IdleTimeout:       60 * time.Second, // keep-alive connections waiting for the next request
		MaxHeaderBytes:    1 << 16,
		// Route net/http's own diagnostics (TLS handshake errors, ...) through slog too.
		ErrorLog: slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		// Request contexts derive from handlerCtx, which stays alive during graceful shutdown.
		// (Otherwise Ctrl-C would cancel the context of in-flight requests, defeating the point
		// of draining them.)
		BaseContext: func(net.Listener) context.Context { return handlerCtx },
	}

	// Listen separately from Serve so a bad address fails here, synchronously, and so we can log
	// the real address (useful with ":0").
	ln, err := net.Listen("tcp", cfg.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.Addr, err)
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	log.Info("listening", "addr", ln.Addr().String())

	select {
	case err := <-serveErr:
		return fmt.Errorf("serve: %w", err) // Serve only returns early on a real failure
	case <-ctx.Done():
	}

	// --- Graceful shutdown ------------------------------------------------------------------
	log.Info("shutdown signal received, draining", "timeout", cfg.ShutdownTimeout.String())
	// 1. Report not-ready so the load balancer stops routing new requests here. (In Kubernetes
	//    you would also pause a few seconds here to let the endpoint removal propagate.)
	draining.Store(true)
	// 2. Stop accepting connections and wait for in-flight requests, up to the timeout. The
	//    deadline context must NOT derive from ctx: ctx is already cancelled.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.ShutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		srv.Close() // timeout hit: stop waiting and drop remaining connections
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("serve: %w", err)
	}
	// 3. Only now is it safe to close the database (deferred above): no handler can still use it.
	log.Info("shutdown complete")
	return nil
}

// buildHandler assembles the dependency graph. Each constructor receives exactly what it needs.
func buildHandler(ctx context.Context, cfg config.Config, log *slog.Logger, db *sql.DB, draining *atomic.Bool) (http.Handler, error) {
	st := store.New(db) // *store.Store satisfies the UserRepo/ProjectRepo/TaskRepo interfaces

	// Argon2id with the OWASP-recommended parameters (see auth.DefaultParams).
	hasher, err := auth.NewPasswordHasher(auth.DefaultParams)
	if err != nil {
		return nil, err
	}
	issuer, err := auth.NewIssuer([]byte(cfg.JWTSecret.Reveal()), cfg.JWTTTL, nil)
	if err != nil {
		return nil, err
	}
	authSvc, err := service.NewAuthService(st, st, hasher, issuer, log)
	if err != nil {
		return nil, err
	}
	// The second authentication mode: database-backed cookie sessions. The console mailer only
	// LOGS emails (including their links); swap in a real implementation for production.
	sessionSvc, err := service.NewSessionAuthService(service.SessionAuthDeps{
		Auth: authSvc, Users: st, Accounts: st, Sessions: st, Verifications: st,
		Hasher: hasher, Mailer: mailer.Console{Log: log}, Log: log,
		Config: service.SessionConfig{TTL: cfg.SessionTTL, UpdateAge: cfg.SessionUpdateAge, PublicURL: cfg.PublicURL},
	})
	if err != nil {
		return nil, err
	}
	var github *auth.GitHub // nil = "Sign in with GitHub" disabled
	if cfg.GitHubEnabled() {
		github, err = auth.NewGitHub(auth.GitHubConfig{
			ClientID: cfg.GitHubClientID, ClientSecret: cfg.GitHubClientSecret.Reveal(),
			RedirectURL: cfg.PublicURL + "/v1/session-auth/oauth/github/callback",
		})
		if err != nil {
			return nil, err
		}
	}
	if !cfg.CookieSecure {
		log.Warn("session cookies are sent WITHOUT the Secure flag; use this for local http development only")
	}

	return httpapi.NewHandler(ctx, httpapi.Deps{
		Logger:      log,
		Auth:        authSvc,
		Projects:    service.NewProjectService(st),
		Tasks:       service.NewTaskService(st, st),
		Tokens:      issuer,
		DB:          db,
		Draining:    draining,
		RateRPS:     cfg.RateRPS,
		RateBurst:   cfg.RateBurst,
		CORSOrigins: cfg.CORSOrigins,

		SessionAuth:   sessionSvc,
		GitHub:        github,
		CookieSecure:  cfg.CookieSecure,
		AuthRateRPS:   cfg.AuthRateRPS,
		AuthRateBurst: cfg.AuthRateBurst,
	}), nil
}

// newLogger builds the structured logger: JSON for production (machine-parsed by Loki,
// CloudWatch, Datadog...), text for humans at a terminal.
func newLogger(cfg config.Config, w io.Writer) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "text" {
		return slog.New(slog.NewTextHandler(w, opts))
	}
	return slog.New(slog.NewJSONHandler(w, opts))
}
