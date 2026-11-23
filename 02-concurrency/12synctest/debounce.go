// Package debounce is the subject of lesson 12 (testing/synctest).
//
// A debouncer delays an action until calls have stopped arriving for a quiet
// period. Typical use: a user types in a search box, and you want to run the
// search once, 300ms after the LAST keystroke, not on every key.
//
// Testing code like this with real clocks is painful: tests would sleep for real
// (slow) and assertions near the boundary would be flaky. testing/synctest
// (stable since Go 1.25) fixes that. See debounce_test.go for the details.
//
// In industry: debouncing/throttling, retry backoff, cache expiry, heartbeats
// and timeouts are all time-driven; synctest lets you test them in microseconds
// and deterministically, with no injected "Clock" interface needed.
package debounce

import (
	"sync"
	"time"
)

// Debouncer runs fn once, `wait` after the most recent Trigger call.
type Debouncer struct {
	wait time.Duration
	fn   func()

	mu    sync.Mutex  // guards timer
	timer *time.Timer // nil when nothing is pending
}

// New returns a Debouncer that calls fn after wait of quiet time.
func New(wait time.Duration, fn func()) *Debouncer {
	return &Debouncer{wait: wait, fn: fn}
}

// Trigger schedules fn, replacing (resetting) any pending schedule.
func (d *Debouncer) Trigger() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop() // cancel the previous pending call
	}
	d.timer = time.AfterFunc(d.wait, d.fn)
}

// Stop cancels a pending call, if any.
func (d *Debouncer) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.timer != nil {
		d.timer.Stop()
		d.timer = nil
	}
}
