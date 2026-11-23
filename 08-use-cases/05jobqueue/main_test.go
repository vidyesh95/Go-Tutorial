package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func newTestQueue(t *testing.T) *Queue {
	t.Helper()
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	q := NewQueue(db, slog.New(slog.NewTextHandler(io.Discard, nil)))
	q.visibility, q.poll, q.backoff, q.reapEvery = 100*time.Millisecond, 2*time.Millisecond, 5*time.Millisecond, 10*time.Millisecond
	return q
}

func status(t *testing.T, q *Queue, id int64) (string, int) {
	t.Helper()
	var s string
	var attempts int
	if err := q.db.QueryRowContext(t.Context(), `SELECT status, attempts FROM jobs WHERE id=?`, id).Scan(&s, &attempts); err != nil {
		t.Fatal(err)
	}
	return s, attempts
}

// waitDrained polls until no job is queued or running (or fails the test).
func waitDrained(t *testing.T, q *Queue) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if n, err := q.Pending(t.Context()); err == nil && n == 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("queue did not drain")
}

// The core guarantee of the atomic claim: with many workers racing, no job is handed out twice.
func TestClaimGivesEachJobToExactlyOneWorker(t *testing.T) {
	q := newTestQueue(t)
	const jobs, workers = 60, 8
	for i := range jobs {
		if _, err := q.Enqueue(t.Context(), "noop", i); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	claimed := map[int64]int{}
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for {
				job, ok, err := q.claim(t.Context(), time.Minute)
				if err != nil {
					t.Errorf("claim: %v", err)
					return
				}
				if !ok {
					return
				}
				mu.Lock()
				claimed[job.ID]++
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	if len(claimed) != jobs {
		t.Errorf("claimed %d distinct jobs, want %d", len(claimed), jobs)
	}
	for id, n := range claimed {
		if n != 1 {
			t.Errorf("job %d claimed %d times", id, n)
		}
	}
}

func TestRetryWithBackoffThenDeadLetter(t *testing.T) {
	q := newTestQueue(t)
	var calls atomic.Int32
	q.Register("always_fails", func(context.Context, Job) error {
		calls.Add(1)
		return errors.New("boom")
	})
	id, _ := q.Enqueue(t.Context(), "always_fails", nil, WithMaxAttempts(3))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx, 2) }()
	waitDrained(t, q)
	cancel()
	<-done

	if s, attempts := status(t, q, id); s != "dead" || attempts != 3 {
		t.Errorf("status=%s attempts=%d, want dead/3", s, attempts)
	}
	if calls.Load() != 3 {
		t.Errorf("handler ran %d times, want 3", calls.Load())
	}
}

func TestRetryDelayDoubles(t *testing.T) {
	base := 100 * time.Millisecond
	want := []time.Duration{100, 200, 400, 800}
	for i, w := range want {
		if got := retryDelay(base, i+1); got != w*time.Millisecond {
			t.Errorf("attempt %d: delay %v, want %v", i+1, got, w*time.Millisecond)
		}
	}
	if retryDelay(base, 1000) <= 0 { // the shift is capped, so it can never overflow to <= 0
		t.Error("huge attempt numbers must not overflow")
	}
}

func TestPermanentErrorSkipsRetries(t *testing.T) {
	q := newTestQueue(t)
	q.Register("bad", func(context.Context, Job) error { return fmt.Errorf("%w: nope", ErrPermanent) })
	id, _ := q.Enqueue(t.Context(), "bad", nil, WithMaxAttempts(5))

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx, 1) }()
	waitDrained(t, q)
	cancel()
	<-done

	if s, attempts := status(t, q, id); s != "dead" || attempts != 1 {
		t.Errorf("status=%s attempts=%d, want dead/1", s, attempts)
	}
}

func TestPanickingHandlerDoesNotKillWorker(t *testing.T) {
	q := newTestQueue(t)
	q.Register("panics", func(context.Context, Job) error { panic("oops") })
	q.Register("ok", func(context.Context, Job) error { return nil })
	bad, _ := q.Enqueue(t.Context(), "panics", nil, WithMaxAttempts(1))
	good, _ := q.Enqueue(t.Context(), "ok", nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx, 1) }() // ONE worker must survive the panic and do the next job
	waitDrained(t, q)
	cancel()
	<-done

	if s, _ := status(t, q, bad); s != "dead" {
		t.Errorf("panicking job status = %s, want dead", s)
	}
	if s, _ := status(t, q, good); s != "succeeded" {
		t.Errorf("following job status = %s, want succeeded", s)
	}
}

func TestVisibilityTimeoutRecoversCrashedJob(t *testing.T) {
	q := newTestQueue(t)
	var ran atomic.Int32
	q.Register("work", func(context.Context, Job) error { ran.Add(1); return nil })
	id, _ := q.Enqueue(t.Context(), "work", nil)

	// A worker claims the job with a 20ms lease and "crashes".
	if _, ok, err := q.claim(t.Context(), 20*time.Millisecond); err != nil || !ok {
		t.Fatalf("claim ok=%v err=%v", ok, err)
	}
	if s, _ := status(t, q, id); s != "running" {
		t.Fatalf("status = %s, want running", s)
	}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx, 1) }()
	waitDrained(t, q)
	cancel()
	<-done

	if s, attempts := status(t, q, id); s != "succeeded" || attempts != 2 {
		t.Errorf("status=%s attempts=%d, want succeeded/2 (one lost attempt + one real)", s, attempts)
	}
}

// A worker that lost its lease must not overwrite the newer attempt's outcome.
func TestFencingIgnoresStaleCompletion(t *testing.T) {
	q := newTestQueue(t)
	id, _ := q.Enqueue(t.Context(), "work", nil)

	stale, _, _ := q.claim(t.Context(), time.Millisecond) // attempt 1, lease about to expire
	time.Sleep(5 * time.Millisecond)
	if n, err := q.reap(t.Context()); err != nil || n != 1 {
		t.Fatalf("reap n=%d err=%v", n, err)
	}
	fresh, ok, _ := q.claim(t.Context(), time.Minute) // attempt 2 by another worker
	if !ok || fresh.Attempts != 2 {
		t.Fatalf("expected attempt 2, got %+v ok=%v", fresh, ok)
	}

	// The slow first worker finally reports "failed". It must be ignored.
	if err := q.fail(t.Context(), stale, errors.New("late failure")); err != nil {
		t.Fatal(err)
	}
	if s, _ := status(t, q, id); s != "running" {
		t.Errorf("stale result changed the job: status = %s, want running", s)
	}
	if err := q.complete(t.Context(), fresh); err != nil {
		t.Fatal(err)
	}
	if s, _ := status(t, q, id); s != "succeeded" {
		t.Errorf("status = %s, want succeeded", s)
	}
}

func TestGracefulShutdownFinishesInFlightJob(t *testing.T) {
	q := newTestQueue(t)
	started := make(chan struct{})
	q.Register("slow", func(ctx context.Context, _ Job) error {
		close(started)
		time.Sleep(80 * time.Millisecond) // deliberately ignores ctx: shutdown must not abort it
		return nil
	})
	id, _ := q.Enqueue(t.Context(), "slow", nil)

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); q.Run(ctx, 2) }()
	<-started
	cancel() // shutdown while the job is mid-flight
	<-done   // Run must wait for it

	if s, _ := status(t, q, id); s != "succeeded" {
		t.Errorf("in-flight job status = %s, want succeeded", s)
	}
}

func TestDelayedJobWaitsForRunAt(t *testing.T) {
	q := newTestQueue(t)
	q.Enqueue(t.Context(), "x", nil, WithDelay(time.Hour))
	if _, ok, err := q.claim(t.Context(), time.Minute); err != nil || ok {
		t.Errorf("a job scheduled for later must not be claimable (ok=%v err=%v)", ok, err)
	}
}
