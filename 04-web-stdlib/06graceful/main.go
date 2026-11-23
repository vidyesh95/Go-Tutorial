// Lesson 06: graceful shutdown
//
// Deploys, autoscaling and node drains all stop your process with SIGTERM (Ctrl-C sends
// SIGINT). If you just exit, every in-flight request dies mid-response and users see
// errors. Graceful shutdown means: stop accepting NEW work, let IN-FLIGHT work finish
// (within a deadline), then exit.
//
// The recipe used here:
//
//  1. ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
//     ctx is cancelled on the first signal. After the first signal we call stop() so
//     that a SECOND Ctrl-C restores default behaviour and kills the process at once
//     (the escape hatch when shutdown hangs).
//  2. Serve in a goroutine; main waits for either a Serve error or ctx.Done().
//  3. Flip readiness to "not ready" so load balancers stop sending traffic (see below).
//  4. srv.Shutdown(timeoutCtx): closes listeners, closes idle connections, waits for
//     active requests to finish, or returns ctx.Err() when the deadline passes.
//     Serve() returns http.ErrServerClosed IMMEDIATELY when Shutdown is called: that is
//     expected, not a failure. But main must wait for Shutdown to return, or the process
//     exits before in-flight requests complete (a classic bug).
//  5. If Shutdown timed out, force it: cancel the base context (handlers watching
//     r.Context() see it) and call srv.Close().
//
// BaseContext: srv.BaseContext supplies the root context of EVERY request context. Shutdown
// does not cancel request contexts (that is why in-flight work can finish); a request's
// context is cancelled when its client disconnects or its handler returns. By deriving
// every request from our own baseCtx we get a switch that, in the forced path, tells
// all remaining handlers "stop now". Handlers must still pass r.Context() down
// to DB/HTTP calls for this to have any effect.
//
// Readiness vs liveness (Kubernetes):
//   - /healthz (liveness): "is the process wedged?" Failing it makes kubelet RESTART the
//     container. Keep it dumb: return 200 if the server can answer at all.
//   - /readyz (readiness): "should I receive traffic?" Failing it removes the pod from the
//     Service endpoints; no restart. Also fail it while starting up (cache warm, DB
//     connected) and while draining.
//     Endpoint removal is asynchronous: for a moment after SIGTERM, load balancers and kube-proxy
//     may still route new requests to you. So: flip readiness to 503, SLEEP a few seconds
//     (drainDelay) while still serving, and only then call Shutdown. Make
//     terminationGracePeriodSeconds (default 30s) larger than drainDelay + shutdownTimeout,
//     or SIGKILL arrives mid-shutdown.
//
// Things Shutdown does NOT cover: hijacked connections (WebSockets) and background goroutines
// you started. Use srv.RegisterOnShutdown for the former and a WaitGroup/errgroup for the latter.
//
// Default mode runs a scripted demo (it sends a real SIGTERM to itself, handled through the
// same signal.NotifyContext path as production):
//
//	go run ./04-web-stdlib/06graceful
//
// With -serve it listens on :8080 until you press Ctrl-C:
//
//	go run ./04-web-stdlib/06graceful -serve
//	curl -i localhost:8080/readyz
//	curl -i localhost:8080/slow       # then press Ctrl-C in the server terminal: this still completes
//
// In industry: every long-running Go service has this block in main (often via an
// errgroup that also stops workers, consumers and the metrics server). The same ctx is
// threaded into everything started from main, so one signal stops the whole process in order.
package main

import (
	"context"
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
)

type config struct {
	drainDelay      time.Duration // keep serving (but not ready) so load balancers notice
	shutdownTimeout time.Duration // max wait for in-flight requests
}

type app struct {
	logger   *slog.Logger
	ready    atomic.Bool  // read by /readyz on many goroutines: atomic, not a plain bool
	inflight atomic.Int32 // for the demo: how many slow handlers are running
	slowFor  time.Duration
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "alive\n")
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		if !a.ready.Load() {
			http.Error(w, "shutting down", http.StatusServiceUnavailable)
			return
		}
		io.WriteString(w, "ready\n")
	})

	// A request that takes a while. It ignores shutdown on purpose (Shutdown waits for it)
	// but stops if the client leaves or the base context is cancelled in the forced path.
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		a.inflight.Add(1)
		defer a.inflight.Add(-1)
		select {
		case <-time.After(a.slowFor):
			io.WriteString(w, "slow request finished normally\n")
		case <-r.Context().Done():
			a.logger.Warn("slow handler aborted", "cause", r.Context().Err())
		}
	})

	// A request that never finishes by itself: used to show the forced path.
	mux.HandleFunc("GET /stuck", func(w http.ResponseWriter, r *http.Request) {
		a.inflight.Add(1)
		defer a.inflight.Add(-1)
		<-r.Context().Done()
		a.logger.Warn("stuck handler aborted", "cause", r.Context().Err())
	})
	return mux
}

// run serves on ln until ctx is cancelled, then shuts down gracefully. It returns nil on a
// clean shutdown, or an error if serving failed or the shutdown deadline was exceeded.
func (a *app) run(ctx context.Context, ln net.Listener, cfg config) error {
	baseCtx, baseCancel := context.WithCancel(context.Background())
	defer baseCancel()

	srv := &http.Server{
		Handler:           a.routes(),
		ReadHeaderTimeout: 5 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return baseCtx },
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()
	a.ready.Store(true)
	a.logger.Info("serving", "addr", ln.Addr().String())

	select {
	case err := <-serveErr: // Serve failed on its own (e.g. listener closed)
		return err
	case <-ctx.Done():
		a.logger.Info("shutdown signal received")
	}

	// Step 3: tell load balancers to stop sending traffic, and keep serving while they notice.
	a.ready.Store(false)
	a.logger.Info("readiness off, draining", "delay", cfg.drainDelay)
	time.Sleep(cfg.drainDelay)

	// Step 4: the real shutdown. Use a FRESH context: ctx is already cancelled.
	shutCtx, cancel := context.WithTimeout(context.Background(), cfg.shutdownTimeout)
	defer cancel()
	a.logger.Info("shutting down server", "timeout", cfg.shutdownTimeout)
	if err := srv.Shutdown(shutCtx); err != nil {
		// Step 5: out of patience. Cancel request contexts, then drop connections.
		a.logger.Error("graceful shutdown timed out, forcing", "err", err, "inflight", a.inflight.Load())
		baseCancel()
		srv.Close()
		<-serveErr
		return fmt.Errorf("graceful shutdown failed: %w", err)
	}
	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	a.logger.Info("server stopped cleanly")
	return nil
}

func newApp(slowFor time.Duration, w io.Writer) *app {
	logger := slog.New(slog.NewTextHandler(w, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{} // no timestamps: keeps lesson output readable
			}
			return a
		},
	}))
	return &app{logger: logger, slowFor: slowFor}
}

func main() {
	serve := flag.Bool("serve", false, "run a real server on :8080 until Ctrl-C / SIGTERM")
	flag.Parse()

	// Step 1. Every call site below gets ctx; nothing reads signals but this line.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// After the first signal restore default handling: a second Ctrl-C kills us immediately.
	context.AfterFunc(ctx, stop)

	if *serve {
		ln, err := net.Listen("tcp", ":8080")
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		a := newApp(5*time.Second, os.Stdout)
		cfg := config{drainDelay: 2 * time.Second, shutdownTimeout: 10 * time.Second}
		if err := a.run(ctx, ln, cfg); err != nil {
			a.logger.Error("run failed", "err", err)
			os.Exit(1)
		}
		return
	}
	demo(ctx)
}

// ---------- demo ----------

func get(c *http.Client, url string) string {
	resp, err := c.Get(url)
	if err != nil {
		return "ERROR: " + err.Error()
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("%s %q", resp.Status, string(b))
}

func waitFor(cond func() bool) {
	for deadline := time.Now().Add(2 * time.Second); !cond() && time.Now().Before(deadline); {
		time.Sleep(5 * time.Millisecond)
	}
}

func demo(sigCtx context.Context) {
	// DisableKeepAlives so each probe is a fresh connection and tells us about the listener.
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

	fmt.Println("=== A. clean shutdown: the in-flight request finishes")
	a := newApp(400*time.Millisecond, os.Stdout)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	base := "http://" + ln.Addr().String()
	done := make(chan error, 1)
	go func() {
		done <- a.run(sigCtx, ln, config{drainDelay: 150 * time.Millisecond, shutdownTimeout: 2 * time.Second})
	}()
	waitFor(a.ready.Load)
	fmt.Println("GET /readyz  ->", get(client, base+"/readyz"))

	slowResult := make(chan string, 1)
	go func() { slowResult <- get(client, base+"/slow") }()
	waitFor(func() bool { return a.inflight.Load() == 1 })

	fmt.Println(">>> sending SIGTERM to ourselves while /slow is in flight")
	if p, err := os.FindProcess(os.Getpid()); err != nil || p.Signal(syscall.SIGTERM) != nil {
		fmt.Println("(cannot signal on this OS; run with -serve and press Ctrl-C instead)")
		return
	}
	waitFor(func() bool { return !a.ready.Load() })
	fmt.Println("GET /readyz  ->", get(client, base+"/readyz"), "  (draining: new work is refused via readiness)")
	fmt.Println("GET /slow    ->", <-slowResult, "  (in-flight request completed)")
	fmt.Println("run() returned:", <-done)
	fmt.Println("GET /readyz  ->", get(client, base+"/readyz"), "  (listener is closed)")

	fmt.Println("\n=== B. shutdown deadline exceeded: forced stop")
	ctx, cancel := context.WithCancel(context.Background()) // stands in for a signal
	b := newApp(0, os.Stdout)
	ln, _ = net.Listen("tcp", "127.0.0.1:0")
	base = "http://" + ln.Addr().String()
	go func() {
		done <- b.run(ctx, ln, config{drainDelay: 10 * time.Millisecond, shutdownTimeout: 200 * time.Millisecond})
	}()
	waitFor(b.ready.Load)
	stuck := make(chan string, 1)
	go func() { stuck <- get(client, base+"/stuck") }()
	waitFor(func() bool { return b.inflight.Load() == 1 })
	cancel()
	fmt.Println("GET /stuck   ->", <-stuck)
	fmt.Println("run() returned:", <-done)
}
