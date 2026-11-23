package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// start runs the app on a loopback port and returns its base URL, a "send SIGTERM"
// function (cancelling ctx is what signal.NotifyContext does) and the run() result channel.
func start(t *testing.T, slowFor time.Duration, cfg config) (a *app, base string, term context.CancelFunc, done chan error) {
	t.Helper()
	a = newApp(slowFor, io.Discard)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, term := context.WithCancel(t.Context())
	done = make(chan error, 1)
	go func() { done <- a.run(ctx, ln, cfg) }()
	waitFor(a.ready.Load)
	return a, "http://" + ln.Addr().String(), term, done
}

func newTestClient() *http.Client {
	return &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}
}

func TestInFlightRequestCompletesDuringShutdown(t *testing.T) {
	a, base, term, done := start(t, 300*time.Millisecond, config{drainDelay: 20 * time.Millisecond, shutdownTimeout: 3 * time.Second})
	c := newTestClient()

	type result struct {
		status int
		err    error
	}
	res := make(chan result, 1)
	go func() {
		resp, err := c.Get(base + "/slow")
		if err != nil {
			res <- result{err: err}
			return
		}
		resp.Body.Close()
		res <- result{status: resp.StatusCode}
	}()
	waitFor(func() bool { return a.inflight.Load() == 1 })

	term() // SIGTERM arrives while the request is mid-flight

	if r := <-res; r.err != nil || r.status != http.StatusOK {
		t.Fatalf("in-flight request: status=%d err=%v", r.status, r.err)
	}
	if err := <-done; err != nil {
		t.Errorf("run() = %v, want nil", err)
	}
}

func TestReadinessFlipsBeforeListenerCloses(t *testing.T) {
	a, base, term, done := start(t, 0, config{drainDelay: 300 * time.Millisecond, shutdownTimeout: time.Second})
	c := newTestClient()

	resp, err := c.Get(base + "/readyz")
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("before shutdown: %v %v", resp, err)
	}
	resp.Body.Close()

	term()
	waitFor(func() bool { return !a.ready.Load() })

	// During the drain window the server still answers, but says "not ready".
	resp, err = c.Get(base + "/readyz")
	if err != nil {
		t.Fatalf("server should still accept connections while draining: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("readyz while draining = %d, want 503", resp.StatusCode)
	}
	// Liveness must stay green: failing it would make Kubernetes restart us mid-drain.
	resp, err = c.Get(base + "/healthz")
	if err != nil || resp.StatusCode != 200 {
		t.Errorf("healthz while draining: %v %v", resp, err)
	} else {
		resp.Body.Close()
	}

	if err := <-done; err != nil {
		t.Errorf("run() = %v", err)
	}
	if _, err := c.Get(base + "/readyz"); err == nil {
		t.Error("listener still open after shutdown")
	}
}

func TestShutdownTimeoutForcesStop(t *testing.T) {
	a, base, term, done := start(t, 0, config{drainDelay: time.Millisecond, shutdownTimeout: 100 * time.Millisecond})
	go newTestClient().Get(base + "/stuck")
	waitFor(func() bool { return a.inflight.Load() == 1 })

	begin := time.Now()
	term()
	err := <-done

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("run() = %v, want DeadlineExceeded", err)
	}
	if time.Since(begin) > 2*time.Second {
		t.Error("forced shutdown took too long")
	}
	waitFor(func() bool { return a.inflight.Load() == 0 })
	if n := a.inflight.Load(); n != 0 {
		t.Errorf("handler still running after force stop: %d", n)
	}
}

// Handlers are plain http.Handlers, so probes can also be unit-tested without a server.
func TestReadyzHandler(t *testing.T) {
	a := newApp(0, io.Discard)
	h := a.routes()
	probe := func() int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", "/readyz", nil))
		return rec.Code
	}
	if probe() != 503 {
		t.Error("must not be ready before run() marks it so")
	}
	a.ready.Store(true)
	if probe() != 200 {
		t.Error("ready flag ignored")
	}
}
