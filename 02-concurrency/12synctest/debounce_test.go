package debounce

import (
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

// Lesson 12: testing/synctest
//
// synctest.Test(t, f) runs f in a "bubble":
//   - Goroutines started inside f belong to the bubble.
//   - The time package uses a FAKE CLOCK per bubble. It starts at
//     2000-01-01 00:00:00 UTC and only moves when EVERY goroutine in the bubble
//     is "durably blocked" (sleeping, or blocked on a channel/select/WaitGroup/
//     Cond created inside the bubble). Then the clock jumps straight to the next
//     timer. So time.Sleep(time.Hour) returns instantly, in real time.
//   - synctest.Wait() blocks until all OTHER goroutines in the bubble are
//     durably blocked. Use it to let background work settle before asserting.
//
// Gotchas:
//   - Mutex waits and real I/O (network, files) are NOT durably blocking, so
//     don't mix real sockets with a bubble. Use net.Pipe or fakes.
//   - Leaked goroutines still blocked when f returns make the test panic with a
//     deadlock error, which is useful: synctest also catches goroutine leaks.
//   - Run it like any test: go test -race ./02-concurrency/12synctest

func TestDebounceRunsOnceAfterQuietPeriod(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		d := New(100*time.Millisecond, func() { calls.Add(1) })

		// A burst of triggers, 30ms apart. Each one resets the timer.
		for range 5 {
			d.Trigger()
			time.Sleep(30 * time.Millisecond) // instant: the fake clock jumps
		}
		synctest.Wait()
		if got := calls.Load(); got != 0 {
			t.Fatalf("fn ran during the burst: %d calls, want 0", got)
		}

		// 100ms of quiet time after the last trigger: now it should fire once.
		time.Sleep(100 * time.Millisecond)
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("after quiet period: %d calls, want 1", got)
		}
	})
}

func TestDebounceBoundary(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		d := New(time.Second, func() { calls.Add(1) })
		d.Trigger()

		// With a real clock, asserting "1ns before the deadline" would be flaky.
		// With the fake clock it is exact.
		time.Sleep(time.Second - time.Nanosecond)
		synctest.Wait()
		if got := calls.Load(); got != 0 {
			t.Fatalf("fired 1ns early: %d calls", got)
		}

		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if got := calls.Load(); got != 1 {
			t.Fatalf("did not fire at the deadline: %d calls", got)
		}
	})
}

func TestDebounceStop(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var calls atomic.Int32
		d := New(50*time.Millisecond, func() { calls.Add(1) })
		d.Trigger()
		d.Stop()

		time.Sleep(time.Hour) // an hour of fake time passes instantly
		synctest.Wait()
		if got := calls.Load(); got != 0 {
			t.Fatalf("fn ran after Stop: %d calls", got)
		}
	})
}

func TestFakeClockStartsAtY2K(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		start := time.Now()
		if start.Year() != 2000 {
			t.Fatalf("bubble clock year = %d, want 2000", start.Year())
		}
		time.Sleep(24 * time.Hour)
		if got := time.Since(start); got != 24*time.Hour {
			t.Fatalf("elapsed = %v, want exactly 24h", got)
		}
	})
}
