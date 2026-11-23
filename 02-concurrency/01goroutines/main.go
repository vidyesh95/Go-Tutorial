// Lesson 01: Goroutines
//
// A goroutine is a function running concurrently with the rest of your program.
// Start one by putting the `go` keyword in front of a function call:
//
//	go doWork()
//
// Goroutines are NOT OS threads. They are tiny (a few KB of stack that grows
// on demand) and the Go runtime scheduler multiplexes thousands of them onto a
// small pool of OS threads. Starting one costs roughly a microsecond.
//
// Key rules:
//   - `go f(x)` evaluates f and its arguments NOW, then runs f in a new goroutine.
//   - When main() returns, the whole program exits immediately, killing every
//     other goroutine, finished or not. This is the #1 beginner bug.
//   - Never use time.Sleep to "wait" for goroutines. Use sync.WaitGroup (or a
//     channel) so you wait for exactly as long as needed.
//   - Since Go 1.22 every loop iteration has its own copy of the loop variable,
//     so capturing `i` inside a goroutine is safe (no more `i := i`).
//   - Since Go 1.25, wg.Go(f) replaces the wg.Add(1) + go func(){ defer wg.Done() ... }() dance.
//   - Goroutines run in no guaranteed order. Output below is sorted where it matters.
//
// GOMAXPROCS is the number of OS threads that can run Go code at the same
// time. It defaults to the number of CPUs. Since Go 1.25 it is also
// container-aware: if a Linux container (Kubernetes/Docker) has a CPU limit,
// the default respects that limit and is updated if the limit changes. Before
// 1.25 teams imported go.uber.org/automaxprocs to get this behaviour.
//
// In industry: you rarely write bare `go` statements in business logic. You
// start goroutines at well-defined boundaries (one per request in net/http,
// a few background workers at startup) and always answer two questions: "who
// waits for this goroutine?" and "how does it stop?". A goroutine nobody
// waits for and nobody can stop is a leak. Code review checks for exactly that.
package main

import (
	"fmt"
	"runtime"
	"slices"
	"sync"
)

func main() {
	fmt.Println("== 1. The problem: main exits before the goroutine runs ==")
	// If we wrote `go fmt.Println("hello from goroutine")` and then returned,
	// the program would very likely exit without printing it. We don't run it
	// here because the result would be non-deterministic. The fix is below.

	fmt.Println("\n== 2. Fix: wait with sync.WaitGroup ==")
	var wg sync.WaitGroup

	// wg.Go (Go 1.25+) does Add(1), starts the goroutine, and calls Done()
	// when the function returns, even if it panics.
	wg.Go(func() {
		fmt.Println("hello from a goroutine")
	})
	wg.Wait() // blocks until every wg.Go function has returned

	fmt.Println("\n== 3. Many goroutines, per-iteration loop variable ==")
	var (
		mu      sync.Mutex // protects results (appending from many goroutines is a race otherwise)
		results []int
	)
	for i := range 5 {
		wg.Go(func() {
			// `i` is a fresh variable each iteration (Go 1.22+), so each
			// goroutine sees its own value.
			mu.Lock()
			defer mu.Unlock()
			results = append(results, i*i)
		})
	}
	wg.Wait()
	slices.Sort(results) // goroutines finish in any order; sort for stable output
	fmt.Println("squares:", results)

	fmt.Println("\n== 4. Scheduler facts ==")
	fmt.Println("NumCPU     :", runtime.NumCPU())
	fmt.Println("GOMAXPROCS :", runtime.GOMAXPROCS(0)) // 0 means "just report, don't change"
	fmt.Println("goroutines :", runtime.NumGoroutine(), "(only main is left)")
}
