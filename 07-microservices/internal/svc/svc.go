// Package svc holds the small pieces every service's main and HTTP stack needs: process
// lifecycle (signals, graceful shutdown), env-var config, JSON responses, request IDs and
// access logging. Real organisations ship this as an internal "platform" or "service
// template" library so that all 50 services start, stop and log the same way.
package svc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
	"uuid"
)

// Main runs a service until SIGINT/SIGTERM, then exits non-zero if run failed. SIGTERM is what
// Docker, Kubernetes and systemd send to ask a process to stop; cancelling the context is how
// run learns about it and starts its graceful shutdown.
func Main(name string, run func(ctx context.Context, log *slog.Logger) error) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", name)
	if err := run(ctx, log); err != nil {
		log.Error("exiting", "err", err)
		os.Exit(1)
	}
}

// Getenv returns the environment variable key, or def when it is unset or empty. Configuration
// through the environment is the twelve-factor convention and what docker-compose/Kubernetes set.
func Getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// ServeHTTP serves h on ln until ctx is cancelled, then drains in-flight requests (up to 10s)
// before returning. The caller opens the listener so it can bind ":0" (random port) and learn
// the real address, and so a bad address fails before anything else starts.
func ServeHTTP(ctx context.Context, ln net.Listener, h http.Handler, log *slog.Logger) error {
	srv := &http.Server{
		Handler: h,
		// Never run a production server without timeouts: a client that connects and then
		// sends nothing would hold a goroutine and file descriptor forever (slowloris).
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err) // Serve only returns early on a real failure
	case <-ctx.Done():
	}
	// ctx is already cancelled, so the shutdown deadline must not derive from it.
	shutCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutCtx); err != nil {
		srv.Close() // deadline hit: stop waiting, drop what is left
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// HTTPHealthcheck GETs url and returns an error unless it answers 200. Distroless images have
// no shell, curl or wget, so Docker HEALTHCHECK runs the service binary itself with -healthcheck.
func HTTPHealthcheck(ctx context.Context, url string) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck %s: status %d", url, resp.StatusCode)
	}
	return nil
}

// --- JSON helpers -----------------------------------------------------------------------------

// WriteJSON writes v as a JSON response with the given status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v) // an encode error here means the client went away; nothing to do
}

// WriteError writes the error shape every service in this track uses: {"error": "..."}.
// One consistent shape lets the gateway and clients handle failures from any service the same way.
func WriteError(w http.ResponseWriter, status int, msg string) {
	WriteJSON(w, status, map[string]string{"error": msg})
}

// --- middleware -------------------------------------------------------------------------------

// RequestIDHeader carries a per-request correlation ID across service boundaries. It is not
// tracing (that is traceparent) but is what you grep for in logs: one ID, every service's lines.
const RequestIDHeader = "X-Request-ID"

// RequestID guarantees every request has an X-Request-ID, generating one when the caller sent
// none, and echoes it in the response so a client can quote it in a bug report. The ID is set
// on the REQUEST header, which is what a reverse proxy then forwards downstream.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get(RequestIDHeader)
		if id == "" || len(id) > 64 { // never trust or echo unbounded client input
			id = uuid.NewV7().String()
			r.Header.Set(RequestIDHeader, id)
		}
		w.Header().Set(RequestIDHeader, id)
		next.ServeHTTP(w, r)
	})
}

// statusWriter records the status code so AccessLog can report it after the handler ran.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK // net/http sends an implicit 200 on first Write
	}
	return w.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController (used by ReverseProxy to flush) reach the real writer.
func (w *statusWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// AccessLog logs one line per request: method, path, status, duration and request ID. A handler
// panic is recovered into a 500 so one bad request cannot kill the process.
func AccessLog(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec) // ReverseProxy's signal to drop the connection; let net/http handle it
				}
				log.Error("panic in handler", "panic", rec, "path", r.URL.Path)
				if sw.status == 0 {
					WriteError(sw, http.StatusInternalServerError, "internal error")
				}
			}
			log.Info("request",
				"method", r.Method, "path", r.URL.Path, "status", sw.status,
				"duration", time.Since(start).Round(time.Microsecond).String(),
				"request_id", r.Header.Get(RequestIDHeader))
		}()
		next.ServeHTTP(sw, r)
	})
}

// DialAddr turns a listen address such as ":9090" into one a local client can dial.
func DialAddr(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return listen
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port)
}
