// Lesson 08: Pipelines, fan-out and fan-in
//
// A pipeline is a series of stages connected by channels. Each stage is a
// function that receives from an inbound channel, does some work, and sends on
// an outbound channel it creates and owns (and closes when done).
//
//	generator -> square -> filter -> consumer
//
// Fan-out: start several copies of a slow stage reading from the SAME input
// channel; the runtime distributes values among them.
// Fan-in (merge): combine several channels into one output channel.
//
// Cancellation without leaks: every stage takes a ctx and every send is wrapped
//
//	select { case out <- v: case <-ctx.Done(): return }
//
// so if the consumer stops early (it found what it wanted, or an error
// happened) the upstream goroutines notice and exit instead of blocking forever
// on a send nobody will receive.
//
// Rules of thumb:
//   - A stage closes ONLY the channel it creates (`defer close(out)`).
//   - Stage functions return <-chan T so downstream code cannot send or close.
//   - Order is NOT preserved after fan-out. Tag values or sort at the end if you care.
//
// In industry: streaming ETL (read, transform, write), log processing,
// image pipelines. For plain "do N things at once" teams use errgroup or a worker pool
// instead; hand-built pipelines appear when data truly flows through stages.
package main

import (
	"context"
	"fmt"
	"runtime"
	"slices"
	"sync"
	"time"
)

// generate emits the given numbers. It stops early if ctx is cancelled.
func generate(ctx context.Context, nums ...int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for _, n := range nums {
			select {
			case out <- n:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// square is a stage: in -> in*in.
func square(ctx context.Context, in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for n := range in {
			select {
			case out <- n * n:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// onlyEven is another stage that drops odd values.
func onlyEven(ctx context.Context, in <-chan int) <-chan int {
	out := make(chan int)
	go func() {
		defer close(out)
		for n := range in {
			if n%2 != 0 {
				continue
			}
			select {
			case out <- n:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out
}

// merge fans several input channels into one.
func merge(ctx context.Context, ins ...<-chan int) <-chan int {
	out := make(chan int)
	var wg sync.WaitGroup
	for _, in := range ins {
		wg.Go(func() {
			for n := range in {
				select {
				case out <- n:
				case <-ctx.Done():
					return
				}
			}
		})
	}
	// Close out only after every forwarding goroutine is finished.
	go func() {
		wg.Wait()
		close(out)
	}()
	return out
}

func main() {
	fmt.Println("== 1. Simple pipeline: generate -> square -> onlyEven ==")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for v := range onlyEven(ctx, square(ctx, generate(ctx, 1, 2, 3, 4, 5, 6))) {
		fmt.Println("value:", v)
	}

	fmt.Println("\n== 2. Fan-out (3 square workers) and fan-in (merge) ==")
	src := generate(ctx, 1, 2, 3, 4, 5, 6, 7, 8, 9)
	// Three workers all read from the same src channel.
	workers := make([]<-chan int, 3)
	for i := range workers {
		workers[i] = square(ctx, src)
	}
	var got []int
	for v := range merge(ctx, workers...) {
		got = append(got, v)
	}
	slices.Sort(got) // fan-out loses ordering
	fmt.Println("squares (sorted):", got)

	fmt.Println("\n== 3. Early cancellation: no goroutine leaks ==")
	before := runtime.NumGoroutine()
	ctx2, cancel2 := context.WithCancel(context.Background())
	// An unbounded-ish source: far more values than we will consume.
	big := make([]int, 1000)
	for i := range big {
		big[i] = i
	}
	out := square(ctx2, generate(ctx2, big...))
	for v := range out {
		fmt.Println("took:", v)
		if v >= 9 { // consumer has what it needs after 4 values
			break
		}
	}
	cancel2() // tell the upstream stages to stop
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	fmt.Printf("goroutines before=%d after=%d (equal means nothing leaked)\n", before, after)
}
