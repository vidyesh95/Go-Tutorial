package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestLogger(sb *strings.Builder) *slog.Logger {
	return slog.New(slog.NewTextHandler(sb, nil))
}

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

func newRequest(method, target string) *http.Request { return httptest.NewRequest(method, target, nil) }

func TestIPLimiterPerClientBuckets(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := newIPLimiter(ctx, 0.001, 2) // refill is negligible during the test

	for i := range 2 {
		if !l.allow("10.0.0.1") {
			t.Fatalf("request %d within burst was rejected", i)
		}
	}
	if l.allow("10.0.0.1") {
		t.Fatal("request over burst was allowed")
	}
	// A different client has its own full bucket.
	if !l.allow("10.0.0.2") {
		t.Fatal("second client was throttled by the first")
	}
}

func TestIPLimiterEvictsIdleClients(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	l := newIPLimiter(ctx, 1, 1)

	clock := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	l.now = func() time.Time { return clock }
	l.allow("a")
	clock = clock.Add(2 * time.Minute)
	l.allow("b")
	clock = clock.Add(2 * time.Minute) // a idle 4m, b idle 2m

	if n := l.evictIdle(3 * time.Minute); n != 1 || l.size() != 1 {
		t.Fatalf("evicted %d, size %d; want 1 and 1", n, l.size())
	}
}

// The janitor goroutine must exit when its context is cancelled; otherwise every NewHandler call
// (one per test, or per config reload) would leak a goroutine forever.
func TestIPLimiterJanitorStopsOnCancel(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	l := newIPLimiter(ctx, 1, 1)

	select {
	case <-l.done:
		t.Fatal("janitor exited before cancellation")
	default:
	}
	cancel()
	select {
	case <-l.done:
	case <-time.After(2 * time.Second):
		t.Fatal("janitor goroutine did not stop after context cancellation")
	}
}
