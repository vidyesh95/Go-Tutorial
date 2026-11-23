// Lesson 11: Data races and the race detector
//
// A DATA RACE happens when two goroutines access the same memory at the same
// time, at least one is a write, and there is no synchronisation between them.
// The outcome is undefined: lost updates, corrupted maps, rare crashes
// that only show up in production under load.
//
// Go ships a race detector. Build/run/test with -race:
//
//	go run  -race ./02-concurrency/11race -racy
//	go test -race ./...
//
// It instruments memory accesses and reports races it OBSERVES during that
// run. No report means "none seen", not "none exist", so exercise your
// concurrent code well in tests. It costs ~5-10x CPU and memory, so use it in
// CI and tests, not in production builds.
//
// Expected output of `go run -race ./02-concurrency/11race -racy` (abridged,
// addresses and line numbers vary):
//
//	expected total: 10000
//	mutex  counter: 10000
//	atomic counter: 10000
//	==================
//	WARNING: DATA RACE
//	Read at 0x00c000480028 by goroutine 215:
//	  main.racyCounter.func1()
//	      .../11race/main.go:66 +0x38
//
//	Previous write at 0x00c000480028 by goroutine 222:
//	  main.racyCounter.func1()
//	      .../11race/main.go:66 +0x48
//
//	Goroutine 215 (running) created at:
//	  sync.(*WaitGroup).Go()
//	  main.main()
//	==================
//	racy   counter: 9423 (expected 10000 ; usually less)
//	Found 1 data race(s)
//	exit status 66
//
// Without -race, the racy version still compiles and runs, but prints a wrong
// total (different each run). The bug is silent, which is why the detector matters.
//
// Why does counter++ race? It is really three steps: load, add, store. Two goroutines
// can both load 5, both store 6, and one increment vanishes.
//
// In industry: `go test -race ./...` is a mandatory CI step on most Go teams.
// A race report is treated as a failing build.
package main

import (
	"flag"
	"fmt"
	"sync"
	"sync/atomic"
)

const (
	goroutines = 100
	perRoutine = 100
)

// racyCounter is DELIBERATELY BROKEN. Many goroutines do counter++ on a
// shared variable with no synchronisation.
func racyCounter() int {
	var counter int
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perRoutine {
				counter++ // DATA RACE: unsynchronised read-modify-write
			}
		})
	}
	wg.Wait()
	return counter
}

// safeCounterMutex fixes the race with a mutex.
func safeCounterMutex() int {
	var (
		mu      sync.Mutex
		counter int
		wg      sync.WaitGroup
	)
	for range goroutines {
		wg.Go(func() {
			for range perRoutine {
				mu.Lock()
				counter++
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	return counter
}

// safeCounterAtomic fixes the race with an atomic: smaller and faster for one counter.
func safeCounterAtomic() int64 {
	var counter atomic.Int64
	var wg sync.WaitGroup
	for range goroutines {
		wg.Go(func() {
			for range perRoutine {
				counter.Add(1)
			}
		})
	}
	wg.Wait()
	return counter.Load()
}

func main() {
	racy := flag.Bool("racy", false, "also run the deliberately racy counter (use with -race to see the report)")
	flag.Parse()

	want := goroutines * perRoutine
	fmt.Println("expected total:", want)
	fmt.Println("mutex  counter:", safeCounterMutex())
	fmt.Println("atomic counter:", safeCounterAtomic())

	if *racy {
		// Gated behind a flag so normal runs and tests stay clean.
		fmt.Println("racy   counter:", racyCounter(), "(expected", want, "; usually less)")
	} else {
		fmt.Println("(pass -racy to run the broken version; add -race to see the detector's report)")
	}
}
