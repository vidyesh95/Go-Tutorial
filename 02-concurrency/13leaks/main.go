// Lesson 13: Common concurrency bugs (and their fixes)
//
// Concurrency bugs are rarely compile errors. They are leaks, deadlocks and
// panics that appear under load. The big four:
//
//  1. Goroutine leak from a blocked send/receive.
//     A goroutine waits on a channel nobody will ever use again, so it lives
//     forever, holding its stack and everything it references. A service that
//     leaks one goroutine per request slowly runs out of memory.
//     Fix: give the goroutine a way out (buffered channel so the send never
//     blocks, or a ctx.Done() case) and make sure every goroutine has an owner.
//
//  2. Forgetting to close a channel that is ranged over.
//     `for v := range ch` waits for close(ch). If the sender never closes, the
//     receiver blocks forever ("fatal error: all goroutines are asleep -
//     deadlock!"). Fix: the sender closes when it is done. We show only the
//     fixed version here, because the broken one would deadlock the program.
//
//  3. Closing a channel twice (or sending after close) panics:
//     "panic: close of closed channel" / "send on closed channel".
//     Rules: only the sender closes; with MANY senders, a coordinator closes
//     after wg.Wait(); if several places might close, guard with sync.Once.
//     Often you don't need to close at all: close is only required to end
//     a range loop or broadcast "done".
//
//  4. Copying a sync.Mutex (or WaitGroup, etc.) by value.
//     The copy has its own independent lock state, so it protects nothing.
//     `go vet` reports this ("passes lock by value"). Fix: pointer receivers
//     and pass the struct by pointer.
//
// How to detect leaks: runtime.NumGoroutine() before/after (shown below), a
// pprof goroutine profile (/debug/pprof/goroutine in production), the
// go.uber.org/goleak package in tests, or testing/synctest, which fails
// when bubble goroutines are left blocked.
//
// In industry: goroutine count is a standard dashboard metric. A line that
// climbs and never falls is a leak. Most leaks are a missing ctx check or an
// unbuffered result channel abandoned after a timeout.
package main

import (
	"context"
	"fmt"
	"runtime"
	"sync"
	"time"
)

// settle gives exiting goroutines a moment to be cleaned up by the scheduler
// so NumGoroutine gives a stable reading in this demo.
func settle() { time.Sleep(50 * time.Millisecond) }

// ---------- Bug 1: leak from a blocked send ----------

func slowWork() string {
	time.Sleep(100 * time.Millisecond)
	return "result"
}

// leakyTimeout gives up after 10ms, but the goroutine is still running and
// will block FOREVER on `ch <- ...` because nobody receives any more.
func leakyTimeout() {
	ch := make(chan string) // unbuffered: send needs a receiver
	go func() { ch <- slowWork() }()
	select {
	case <-ch:
	case <-time.After(10 * time.Millisecond):
	}
}

// fixedTimeoutBuffer: capacity 1 means the send completes even with no receiver,
// so the goroutine finishes and is freed. Simplest fix when exactly one value is sent.
func fixedTimeoutBuffer() {
	ch := make(chan string, 1)
	go func() { ch <- slowWork() }()
	select {
	case <-ch:
	case <-time.After(10 * time.Millisecond):
	}
}

// fixedTimeoutCtx: the sender also listens for cancellation, so it can always exit.
func fixedTimeoutCtx() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	ch := make(chan string)
	go func() {
		v := slowWork()
		select {
		case ch <- v:
		case <-ctx.Done(): // nobody is listening: give up instead of blocking
		}
	}()
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

// ---------- Bug 2: range over a channel that is never closed (fix only) ----------

func rangeWithClose() int {
	ch := make(chan int)
	go func() {
		defer close(ch) // THE FIX: without this line, the range below hangs forever
		for i := 1; i <= 3; i++ {
			ch <- i
		}
	}()
	sum := 0
	for v := range ch {
		sum += v
	}
	return sum
}

// ---------- Bug 3: closing twice ----------

func safeCloseOnce() {
	ch := make(chan struct{})
	var once sync.Once
	closeCh := func() { once.Do(func() { close(ch) }) } // idempotent close

	closeCh()
	closeCh() // would panic with a bare close(ch); safe here
	_, ok := <-ch
	fmt.Println("channel closed, receive ok =", ok)

	// Demonstrate the panic safely by recovering from it:
	defer func() { fmt.Println("recovered from bare double close:", recover()) }()
	bare := make(chan int)
	close(bare)
	close(bare) // panic: close of closed channel
}

// ---------- Bug 4: mutex copied by value ----------

type Counter struct {
	mu sync.Mutex
	n  int
}

// Inc has a POINTER receiver, so the real mutex is shared. This is correct.
func (c *Counter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

// BROKEN version (do not do this). `go vet` flags it: "IncBroken passes lock by value".
//
//	func (c Counter) IncBroken() { // value receiver: c is a COPY, including the mutex
//	    c.mu.Lock()                // locks the copy, which nobody else shares
//	    c.n++                      // and the increment is lost on return anyway
//	    c.mu.Unlock()
//	}
//
// Likewise: `c2 := c1` copies the mutex's state. Keep these types behind pointers.

func report(name string, f func()) {
	before := runtime.NumGoroutine()
	for range 10 {
		f()
	}
	settle()
	time.Sleep(150 * time.Millisecond) // let slowWork finish so fixed versions can exit
	after := runtime.NumGoroutine()
	fmt.Printf("%-22s goroutines before=%d after=%d leaked=%d\n", name, before, after, after-before)
}

func main() {
	fmt.Println("== Bug 1: goroutine leak from a blocked send ==")
	report("leakyTimeout", leakyTimeout) // leaked = 10
	report("fixedTimeoutBuffer", fixedTimeoutBuffer)
	report("fixedTimeoutCtx", fixedTimeoutCtx)

	fmt.Println("\n== Bug 2: range needs close (fixed version) ==")
	fmt.Println("sum:", rangeWithClose())

	fmt.Println("\n== Bug 3: closing a channel twice ==")
	safeCloseOnce()

	fmt.Println("\n== Bug 4: mutex must not be copied ==")
	var c Counter
	var wg sync.WaitGroup
	for range 100 {
		wg.Go(c.Inc) // method value on &c (addressable), so the mutex is shared
	}
	wg.Wait()
	fmt.Println("counter:", c.n, "(correct because Inc uses a pointer receiver)")
}
