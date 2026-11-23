// Lesson 02: Channels
//
// A channel is a typed pipe for sending values between goroutines. It is
// Go's primary way to communicate: "Do not communicate by sharing memory;
// instead, share memory by communicating."
//
//	ch := make(chan int)    // unbuffered
//	ch := make(chan int, 3) // buffered, capacity 3
//
// Unbuffered: a send blocks until a receiver is ready, and vice versa. It is a
// hand-off / rendezvous, so both goroutines synchronise at that moment.
// Buffered: a send blocks only when the buffer is full; a receive blocks only
// when it is empty. It decouples producer and consumer speed a little.
//
// Closing:
//   - close(ch) means "no more values will ever be sent".
//   - Only the SENDER closes, never the receiver. Closing is a signal, not cleanup.
//   - Receiving from a closed channel returns buffered values first, then the
//     zero value forever. Use the comma-ok form `v, ok := <-ch` to tell them apart.
//   - `for v := range ch` loops until the channel is closed AND drained.
//   - Sending on a closed channel panics. Closing a closed channel panics.
//
// Directional types document intent and let the compiler enforce it:
//
//	chan<- int  send-only      <-chan int  receive-only
//
// A bidirectional chan converts to either implicitly.
//
// nil channels: a nil channel blocks forever on both send and receive. That
// sounds useless but is a deliberate trick in select (lesson 03): setting a
// channel variable to nil disables that case.
//
// In industry: channels shine for ownership transfer, pipelines, fan-in/fan-out,
// and signalling (done channels, ctx.Done()). For protecting a shared map or
// counter a mutex is usually simpler (see lesson 04). Most production code
// uses channels sparingly: a worker queue here, a shutdown signal there.
package main

import (
	"fmt"
)

// producer can only SEND on out. The compiler rejects a receive here.
// It owns the channel, so it is the one that closes it.
func producer(out chan<- int, n int) {
	defer close(out) // closing tells the consumer "I'm done"
	for i := range n {
		out <- i
	}
}

// consumer can only RECEIVE from in. Ranging ends when in is closed.
func consumer(in <-chan int) int {
	sum := 0
	for v := range in {
		sum += v
	}
	return sum
}

func main() {
	fmt.Println("== 1. Unbuffered: send and receive must meet ==")
	ch := make(chan string)
	go func() {
		ch <- "ping" // blocks here until main receives
	}()
	fmt.Println("received:", <-ch) // blocks until the goroutine sends

	fmt.Println("\n== 2. Buffered: send does not block until full ==")
	buf := make(chan int, 2)
	buf <- 1 // no receiver needed: fits in the buffer
	buf <- 2
	fmt.Printf("len=%d cap=%d (a third send would block forever here)\n", len(buf), cap(buf))
	fmt.Println("got:", <-buf, <-buf)

	fmt.Println("\n== 3. close + range ==")
	nums := make(chan int)
	go producer(nums, 5)
	fmt.Println("sum of 0..4 =", consumer(nums))

	fmt.Println("\n== 4. Comma-ok receive on a closed channel ==")
	c := make(chan int, 1)
	c <- 42
	close(c)
	v, ok := <-c
	fmt.Printf("first : v=%d ok=%t (buffered value still delivered)\n", v, ok)
	v, ok = <-c
	fmt.Printf("second: v=%d ok=%t (closed and empty: zero value, ok=false)\n", v, ok)

	fmt.Println("\n== 5. nil channel ==")
	var nilCh chan int
	fmt.Println("nilCh == nil:", nilCh == nil)
	// `<-nilCh` or `nilCh <- 1` would block forever (deadlock if nothing else runs).
	// select with a default shows it is never ready without hanging:
	select {
	case v := <-nilCh:
		fmt.Println("impossible", v)
	default:
		fmt.Println("receive from nil channel is never ready")
	}
}
