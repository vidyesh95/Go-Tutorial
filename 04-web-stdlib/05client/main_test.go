package main

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// Note: servers from httptest.NewTestServer use an in-memory network, so requests
// must go through ts.Client() (its Transport knows how to dial the server).
//
// instantClient retries without really sleeping and records the delays it would have used.
func instantClient(hc *http.Client, maxAttempts int, delays *[]time.Duration) *Client {
	return &Client{hc: hc, retry: retryPolicy{
		MaxAttempts: maxAttempts,
		BaseDelay:   10 * time.Millisecond,
		MaxDelay:    80 * time.Millisecond,
		wait: func(ctx context.Context, d time.Duration) error {
			*delays = append(*delays, d)
			return ctx.Err()
		},
	}}
}

// countingServer fails the first `failures` calls with `status`, counting every call.
func countingServer(t *testing.T, failures int32, status int, retryAfter string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= failures {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			http.Error(w, "fail", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":7,"name":"Grace"}`))
	}))
	ts.Client() // NewTestServer sets ts.URL lazily, on the first Client() call
	return ts, &calls
}

func TestBackoffGrowsAndIsCapped(t *testing.T) {
	p := retryPolicy{BaseDelay: 10 * time.Millisecond, MaxDelay: 80 * time.Millisecond}
	for attempt := range 30 { // 30 also proves the shift cannot overflow
		upper := min(p.MaxDelay, p.BaseDelay<<min(attempt, 20))
		for range 50 {
			d := p.backoff(attempt)
			if d < upper/2 || d > upper {
				t.Fatalf("attempt %d: delay %v outside [%v, %v]", attempt, d, upper/2, upper)
			}
		}
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	if d, ok := parseRetryAfter("30", now); !ok || d != 30*time.Second {
		t.Errorf("seconds form: %v %v", d, ok)
	}
	date := now.Add(90 * time.Second).UTC().Format(http.TimeFormat)
	if d, ok := parseRetryAfter(date, now); !ok || d != 90*time.Second {
		t.Errorf("date form: %v %v", d, ok)
	}
	if _, ok := parseRetryAfter("soon", now); ok {
		t.Error("garbage accepted")
	}
	if _, ok := parseRetryAfter("", now); ok {
		t.Error("empty accepted")
	}
}

func TestGetRetriesUntilSuccess(t *testing.T) {
	ts, calls := countingServer(t, 2, http.StatusServiceUnavailable, "")
	var delays []time.Duration
	c := instantClient(ts.Client(), 4, &delays)

	var u user
	if err := c.GetJSON(t.Context(), ts.URL, &u); err != nil {
		t.Fatal(err)
	}
	if u.Name != "Grace" || calls.Load() != 3 || len(delays) != 2 {
		t.Errorf("user=%+v calls=%d delays=%v", u, calls.Load(), delays)
	}
}

func TestGivesUpAfterMaxAttempts(t *testing.T) {
	ts, calls := countingServer(t, 100, http.StatusInternalServerError, "")
	var delays []time.Duration
	err := instantClient(ts.Client(), 3, &delays).GetJSON(t.Context(), ts.URL, nil)

	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Fatalf("err = %v, want StatusError 500", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestDoesNotRetryClientErrors(t *testing.T) {
	ts, calls := countingServer(t, 100, http.StatusNotFound, "")
	var delays []time.Duration
	instantClient(ts.Client(), 5, &delays).GetJSON(t.Context(), ts.URL, nil)
	if calls.Load() != 1 {
		t.Errorf("404 was retried: %d calls", calls.Load())
	}
}

func TestPostRetriedOnlyWithIdempotencyKey(t *testing.T) {
	tests := []struct {
		key       string
		wantCalls int32
	}{
		{"", 1},
		{"key-1", 2},
	}
	for _, tt := range tests {
		ts, calls := countingServer(t, 1, http.StatusServiceUnavailable, "")
		var delays []time.Duration
		instantClient(ts.Client(), 3, &delays).PostJSON(t.Context(), ts.URL, map[string]int{"a": 1}, nil, tt.key)
		if calls.Load() != tt.wantCalls {
			t.Errorf("key=%q: calls = %d, want %d", tt.key, calls.Load(), tt.wantCalls)
		}
	}
}

func TestPostBodyIsResentOnRetry(t *testing.T) {
	var bodies []string
	ts := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodies = append(bodies, string(b))
		if len(bodies) == 1 {
			w.WriteHeader(http.StatusBadGateway)
		}
	}))
	ts.Client()
	var delays []time.Duration
	err := instantClient(ts.Client(), 3, &delays).PostJSON(t.Context(), ts.URL, map[string]int{"a": 1}, nil, "k")
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 || bodies[0] != bodies[1] || bodies[0] == "" {
		t.Errorf("bodies = %q", bodies)
	}
}

func TestRetryAfterIsHonouredButCapped(t *testing.T) {
	ts, _ := countingServer(t, 1, http.StatusTooManyRequests, "3600")
	var delays []time.Duration
	if err := instantClient(ts.Client(), 3, &delays).GetJSON(t.Context(), ts.URL, nil); err != nil {
		t.Fatal(err)
	}
	if len(delays) != 1 || delays[0] != 80*time.Millisecond {
		t.Errorf("delays = %v, want exactly MaxDelay (cap)", delays)
	}
}

func TestCancelledContextStopsRetrying(t *testing.T) {
	ts, calls := countingServer(t, 100, http.StatusServiceUnavailable, "")
	ctx, cancel := context.WithCancel(t.Context())
	var delays []time.Duration
	c := instantClient(ts.Client(), 10, &delays)
	c.retry.wait = func(ctx context.Context, d time.Duration) error {
		cancel() // caller gives up while we are waiting
		return ctx.Err()
	}
	err := c.GetJSON(ctx, ts.URL, nil)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1", calls.Load())
	}
}

// Connection reuse is the reason we drain bodies. Count accepted TCP connections.
func TestDrainedBodiesReuseOneConnection(t *testing.T) {
	var conns atomic.Int32
	ts := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("hello"))
	}))
	ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	ts.Start()
	t.Cleanup(ts.Close)

	hc, tr := newHTTPClient(2 * time.Second)
	t.Cleanup(tr.CloseIdleConnections)
	for range 10 {
		resp, err := hc.Get(ts.URL)
		if err != nil {
			t.Fatal(err)
		}
		drainClose(resp.Body)
	}
	if conns.Load() != 1 {
		t.Errorf("used %d connections, want 1", conns.Load())
	}
}
