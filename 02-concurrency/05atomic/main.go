// Lesson 05: sync/atomic
//
// Atomic operations read/modify/write a single value as one indivisible step,
// using CPU instructions instead of locks. They are cheaper than a mutex
// for simple things: counters, flags, and swapping a pointer.
//
// Use the TYPED values (Go 1.19+), not the old atomic.AddInt64(&x, 1) functions:
//   - atomic.Int32 / Int64 / Uint32 / Uint64: Add, Load, Store, Swap, CompareAndSwap
//   - atomic.Bool                            : Load, Store, Swap, CompareAndSwap
//   - atomic.Pointer[T]                      : Load, Store, Swap, CompareAndSwap (type-safe *T)
//   - atomic.Value                           : any type, but must always store the same one
//
// Why typed values are better:
//   - You cannot accidentally read them non-atomically (no raw int64 to touch).
//   - They are always correctly aligned (the old 64-bit functions crashed on 32-bit platforms).
//
// Limits: atomics protect ONE variable. The moment an invariant spans two
// fields ("balance and lastUpdated must change together") you need a mutex.
// Atomics are also easy to misuse, so reach for them only for simple counters,
// flags and read-mostly snapshots.
//
// atomic.Pointer shines for "copy-on-write config": build a new immutable
// struct, then Store the pointer. Readers Load without locking and always see
// a complete, consistent snapshot.
//
// In industry: request counters and metrics, "shutting down" flags, and
// hot-reloadable config pointers. Prometheus-style metrics libraries use
// atomics internally. Everything else usually uses a mutex for clarity.
package main

import (
	"fmt"
	"sync"
	"sync/atomic"
)

type Config struct {
	Version int
	Name    string
}

type MutexCounter struct {
	mu sync.Mutex
	n  int64
}

func (c *MutexCounter) Inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func main() {
	var wg sync.WaitGroup

	fmt.Println("== 1. atomic.Int64 counter vs mutex counter ==")
	var atomicCount atomic.Int64
	var mc MutexCounter
	for range 1000 {
		wg.Go(func() {
			atomicCount.Add(1) // no lock needed
			mc.Inc()
		})
	}
	wg.Wait()
	fmt.Println("atomic counter:", atomicCount.Load())
	fmt.Println("mutex  counter:", mc.n)

	fmt.Println("\n== 2. atomic.Bool as a flag ==")
	var shuttingDown atomic.Bool
	fmt.Println("before:", shuttingDown.Load())
	shuttingDown.Store(true)
	fmt.Println("after :", shuttingDown.Load())

	// CompareAndSwap(old, new): set only if the current value equals old.
	// Handy for "first one wins" logic.
	var started atomic.Bool
	var winners atomic.Int32
	for range 10 {
		wg.Go(func() {
			if started.CompareAndSwap(false, true) {
				winners.Add(1)
			}
		})
	}
	wg.Wait()
	fmt.Println("goroutines that won the CAS:", winners.Load(), "(always exactly 1)")

	fmt.Println("\n== 3. atomic.Pointer for lock-free config swap ==")
	var cfg atomic.Pointer[Config]
	cfg.Store(&Config{Version: 1, Name: "initial"})

	reader := func() *Config { return cfg.Load() } // readers never block
	fmt.Printf("reader sees: %+v\n", *reader())

	// Writers never mutate the existing Config. They publish a new one.
	old := cfg.Swap(&Config{Version: 2, Name: "reloaded"})
	fmt.Printf("swapped out: %+v\n", *old)
	fmt.Printf("reader sees: %+v\n", *reader())

	fmt.Println("\nRule of thumb: atomics for one independent variable, mutex for anything bigger.")
}
