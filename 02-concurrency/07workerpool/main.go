// Lesson 07: Worker pool
//
// A worker pool runs a FIXED number of goroutines that pull jobs from a shared
// channel. It bounds concurrency: with 1,000,000 jobs and 4 workers you still
// have only 4 jobs in flight, so you don't exhaust memory, file descriptors,
// DB connections or the downstream service's patience.
//
//	jobs chan  ->  [worker][worker][worker]  ->  results chan
//
// Shape:
//  1. Create buffered `jobs` and `results` channels.
//  2. Start N workers; each does `for job := range jobs { ... }`.
//  3. Feed jobs, then close(jobs) so the workers' range loops end.
//  4. Close `results` AFTER all workers are done (wg.Wait in its own goroutine),
//     because workers are the senders on results.
//  5. The caller ranges over results until it is closed.
//
// Gotchas:
//   - Whoever sends closes. The producer closes jobs; a "closer" goroutine closes results.
//   - If you feed jobs from main and read results from main, an unbuffered
//     results channel deadlocks. Feed jobs from a separate goroutine (as below),
//     or buffer the channels.
//   - Errors are just data: put an error field in the result struct so one bad
//     job does not kill the pool. Collect them (or use errors.Join) at the end.
//   - Graceful stop: close(jobs) lets workers FINISH queued jobs then exit. For a
//     hard stop, also watch ctx.Done() inside the worker.
//
// In industry: queue consumers (SQS/Kafka/Rabbit), image/video processing,
// bulk API calls and ETL batch jobs all use this. The pool size comes from
// the bottleneck (DB pool size, API rate limit), not from the CPU count alone.
package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"
)

type Job struct {
	ID    int
	Input int
}

type Result struct {
	JobID  int
	Output int
	Err    error
}

// process is the "real work". Inputs that are multiples of 7 fail so we can show error handling.
func process(j Job) (int, error) {
	time.Sleep(5 * time.Millisecond) // pretend to do I/O
	if j.Input%7 == 0 {
		return 0, fmt.Errorf("job %d: input %d is unlucky", j.ID, j.Input)
	}
	return j.Input * j.Input, nil
}

// worker handles jobs until jobs is closed or ctx is cancelled.
func worker(ctx context.Context, id int, jobs <-chan Job, results chan<- Result) {
	for {
		select {
		case <-ctx.Done():
			return // hard stop
		case j, ok := <-jobs:
			if !ok {
				return // jobs closed and drained: graceful stop
			}
			out, err := process(j)
			// Report the result, but don't block forever if the consumer left.
			select {
			case results <- Result{JobID: j.ID, Output: out, Err: err}:
			case <-ctx.Done():
				return
			}
		}
	}
}

func main() {
	const (
		numWorkers = 3
		numJobs    = 15
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	jobs := make(chan Job)
	results := make(chan Result)

	// Start the pool.
	var wg sync.WaitGroup
	for id := range numWorkers {
		wg.Go(func() { worker(ctx, id, jobs, results) })
	}

	// Feed jobs from a separate goroutine so main is free to read results.
	go func() {
		defer close(jobs) // tells workers: no more work is coming
		for i := 1; i <= numJobs; i++ {
			select {
			case jobs <- Job{ID: i, Input: i}:
			case <-ctx.Done():
				return
			}
		}
	}()

	// Close results once every worker has exited (workers are its senders).
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect. Ranging ends when results is closed.
	var (
		all  []Result
		errs []error
	)
	for r := range results {
		if r.Err != nil {
			errs = append(errs, r.Err)
			continue
		}
		all = append(all, r)
	}

	// Results arrive in completion order, not job order. Sort for stable output.
	slices.SortFunc(all, func(a, b Result) int { return a.JobID - b.JobID })
	fmt.Println("== successful results ==")
	for _, r := range all {
		fmt.Printf("job %2d -> %d\n", r.JobID, r.Output)
	}

	fmt.Println("\n== collected errors ==")
	if err := errors.Join(errs...); err != nil {
		fmt.Println(err)
	}
	fmt.Printf("\n%d ok, %d failed, %d workers\n", len(all), len(errs), numWorkers)
}
