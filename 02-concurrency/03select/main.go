// Lesson 03: select
//
// `select` waits on several channel operations at once and runs the one that
// becomes ready first. It is a `switch` for channels.
//
//	select {
//	case v := <-a:   // runs if a has a value
//	case b <- x:     // runs if b can accept x
//	case <-ctx.Done():
//	default:         // runs immediately if nothing else is ready
//	}
//
// Rules:
//   - If several cases are ready, Go picks one at random (fairness, no starvation).
//   - No ready case and no default: select blocks until one is ready.
//   - `default` makes the select NON-blocking: a try-send / try-receive.
//   - time.After(d) returns a channel that delivers once after d, which is the
//     idiomatic timeout. For loops prefer context.WithTimeout or a reused
//     time.Timer, because older Go versions kept each time.After alive until it fired.
//   - A `for { select { ... } }` loop with a done channel is the standard shape
//     of a long-running goroutine that can be told to stop.
//   - `break` inside a select only leaves the select, not the surrounding
//     for loop. Use `return` or a labelled break to exit the loop.
//
// In industry: every long-lived goroutine has a select with a shutdown case
// (usually <-ctx.Done()). Timeouts around slow calls and "send unless the
// consumer is gone" are everyday uses.
package main

import (
	"fmt"
	"time"
)

func main() {
	fmt.Println("== 1. select picks whichever is ready ==")
	fast := make(chan string, 1)
	slow := make(chan string, 1)
	fast <- "fast result"
	select {
	case m := <-fast:
		fmt.Println("got:", m)
	case m := <-slow:
		fmt.Println("got:", m)
	}

	fmt.Println("\n== 2. default: non-blocking receive and send ==")
	ch := make(chan int, 1)
	select {
	case v := <-ch:
		fmt.Println("received", v)
	default:
		fmt.Println("nothing to receive right now")
	}
	ch <- 1 // fill the buffer
	select {
	case ch <- 2:
		fmt.Println("sent 2")
	default:
		fmt.Println("buffer full, dropped the value instead of blocking")
	}

	fmt.Println("\n== 3. Timeout with time.After ==")
	result := make(chan string)
	go func() {
		time.Sleep(200 * time.Millisecond) // pretend this is a slow call
		result <- "finally done"           // nobody is listening by then: see note below
	}()
	select {
	case r := <-result:
		fmt.Println("got:", r)
	case <-time.After(50 * time.Millisecond):
		fmt.Println("timeout: gave up after 50ms")
	}
	// NOTE: the goroutine above is now stuck on `result <- ...` forever (a leak!).
	// Lesson 13 shows the fix: buffer the channel or use a context.

	fmt.Println("\n== 4. for-select loop with a done channel ==")
	done := make(chan struct{}) // struct{} carries no data: it is purely a signal
	ticks := make(chan int)
	stopped := make(chan struct{})

	go func() {
		defer close(stopped)
		for {
			select {
			case n := <-ticks:
				fmt.Println("worker got tick", n)
			case <-done:
				fmt.Println("worker: done signal received, exiting")
				return // return, not break: break would only leave the select
			}
		}
	}()

	for i := 1; i <= 3; i++ {
		ticks <- i
	}
	close(done) // closing a channel wakes EVERY receiver: a broadcast
	<-stopped   // wait until the worker has really exited
	fmt.Println("main: worker stopped cleanly")
}
