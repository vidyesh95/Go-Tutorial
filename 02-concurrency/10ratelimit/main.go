// Lesson 10: Rate limiting and concurrency limiting
//
// Two different problems that are often confused:
//   - RATE limiting:        "at most N operations per second" (protect an API quota)
//   - CONCURRENCY limiting: "at most N operations running at once" (protect memory/connections)
//
// 1. time.Ticker: the simplest rate limiter. Receive from ticker.C before each
// operation and you get a steady pace. No bursts. Always defer ticker.Stop().
//
// 2. golang.org/x/time/rate: a token bucket. The bucket holds up to `burst`
// tokens and refills at `rate` tokens per second. Each operation takes a token.
//
//	limiter := rate.NewLimiter(rate.Every(100*time.Millisecond), 2) // 10/s, burst 2
//	limiter.Allow()   // non-blocking: true if a token was available, else false
//	limiter.Wait(ctx) // blocks until a token is available or ctx is done
//	limiter.Reserve() // advanced: learn how long you would have to wait
//
// Use Allow to REJECT excess traffic (HTTP 429). Use Wait to SLOW DOWN a
// client calling someone else's API.
//
// 3. Semaphore via buffered channel: a channel of capacity N. Send to acquire a
// slot (blocks when full), receive to release. This limits concurrency, not rate.
// (errgroup.SetLimit or golang.org/x/sync/semaphore do the same job.)
//
// In industry: per-client API rate limiting in middleware (often keyed by IP or
// API key in a map of limiters, or in Redis for multi-instance setups), and
// client-side limiters so batch jobs respect third-party quotas. Concurrency
// semaphores cap outbound calls and DB-heavy work.
package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

func main() {
	fmt.Println("== 1. time.Ticker limiter: one op every 20ms ==")
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	for i := 1; i <= 4; i++ {
		<-ticker.C // wait for the next tick before doing work
		fmt.Printf("op %d at ~%dms\n", i, time.Since(start).Round(10*time.Millisecond).Milliseconds())
	}

	fmt.Println("\n== 2a. rate.Limiter.Allow: reject when out of tokens ==")
	// Refill 1 token per 50ms, bucket holds a burst of 3.
	limiter := rate.NewLimiter(rate.Every(50*time.Millisecond), 3)
	for i := 1; i <= 5; i++ {
		// The first 3 calls drain the full bucket (burst); the rest are rejected.
		fmt.Printf("request %d allowed: %t\n", i, limiter.Allow())
	}

	fmt.Println("\n== 2b. rate.Limiter.Wait: block until a token is free ==")
	waiter := rate.NewLimiter(rate.Every(30*time.Millisecond), 1)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start = time.Now()
	for i := 1; i <= 3; i++ {
		if err := waiter.Wait(ctx); err != nil { // returns an error if ctx ends first
			fmt.Println("wait failed:", err)
			return
		}
		fmt.Printf("call %d proceeded at ~%dms\n", i, time.Since(start).Round(10*time.Millisecond).Milliseconds())
	}

	fmt.Println("\n== 3. Semaphore: at most 3 concurrent workers ==")
	const maxConcurrent = 3
	sem := make(chan struct{}, maxConcurrent)
	var running, peak atomic.Int32
	var wg sync.WaitGroup
	for range 9 {
		wg.Go(func() {
			sem <- struct{}{}        // acquire: blocks while 3 are already inside
			defer func() { <-sem }() // release
			now := running.Add(1)
			defer running.Add(-1)
			// Record the highest concurrency we observed (CAS loop for max).
			for {
				p := peak.Load()
				if now <= p || peak.CompareAndSwap(p, now) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
		})
	}
	wg.Wait()
	fmt.Printf("peak concurrency: %d (limit %d)\n", peak.Load(), maxConcurrent)
}
