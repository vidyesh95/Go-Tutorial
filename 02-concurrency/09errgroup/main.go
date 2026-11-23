// Lesson 09: errgroup
//
// golang.org/x/sync/errgroup is a sync.WaitGroup that also collects the FIRST
// non-nil error returned by any goroutine, (with WithContext) cancels a shared
// context when that happens, and (with SetLimit) bounds how many goroutines
// run at once.
//
//	g, ctx := errgroup.WithContext(parent)
//	g.SetLimit(3)
//	g.Go(func() error { ... })       // blocks if 3 are already running
//	if err := g.Wait(); err != nil { // waits for all, returns the first error
//
// Rules and gotchas:
//   - Only the FIRST error is returned; later ones are dropped. Log them
//     inside the goroutine if you need them all (or use errors.Join yourself).
//   - Cancelling ctx does not stop goroutines by itself: each one must check
//     ctx (pass it to your HTTP/DB calls or select on ctx.Done()).
//   - The derived ctx is cancelled when Wait returns, too: do not use it afterwards.
//   - Writing to distinct slice indexes from different goroutines is safe
//     (no two touch the same element), which gives ordered results without a mutex.
//   - SetLimit must be called before the first Go. g.TryGo is the non-blocking variant.
//
// In industry: errgroup is the go-to for "fetch these N things concurrently,
// fail fast if one breaks": aggregating several backend calls into one API
// response, parallel file uploads, starting several servers together and
// shutting all down if any one fails.
package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"golang.org/x/sync/errgroup"
)

// fakeFetch pretends to call a URL: it takes `latency`, and fails for a "bad" URL.
// It honours ctx, as every real network call should.
func fakeFetch(ctx context.Context, url string, latency time.Duration) (string, error) {
	select {
	case <-time.After(latency):
	case <-ctx.Done():
		return "", fmt.Errorf("fetch %s aborted: %w", url, ctx.Err())
	}
	if url == "https://bad.example.com" {
		return "", errors.New("fetch " + url + ": 500 internal server error")
	}
	return "body of " + url, nil
}

type site struct {
	url     string
	latency time.Duration
}

func main() {
	fmt.Println("== 1. All succeed, bounded concurrency ==")
	sites := []site{
		{"https://a.example.com", 30 * time.Millisecond},
		{"https://b.example.com", 20 * time.Millisecond},
		{"https://c.example.com", 10 * time.Millisecond},
		{"https://d.example.com", 15 * time.Millisecond},
	}

	g, ctx := errgroup.WithContext(context.Background())
	g.SetLimit(2) // at most 2 fetches at a time

	bodies := make([]string, len(sites)) // each goroutine writes only its own index
	for i, s := range sites {
		g.Go(func() error {
			body, err := fakeFetch(ctx, s.url, s.latency)
			if err != nil {
				return err
			}
			bodies[i] = body
			return nil
		})
	}
	if err := g.Wait(); err != nil {
		fmt.Println("unexpected error:", err)
	}
	for i, b := range bodies {
		fmt.Printf("[%d] %s\n", i, b)
	}

	fmt.Println("\n== 2. One fails: first error wins and cancels the rest ==")
	sites = []site{
		{"https://slow1.example.com", 500 * time.Millisecond},
		{"https://bad.example.com", 20 * time.Millisecond}, // fails fast
		{"https://slow2.example.com", 500 * time.Millisecond},
	}
	start := time.Now()
	g, ctx = errgroup.WithContext(context.Background())
	for _, s := range sites {
		g.Go(func() error {
			_, err := fakeFetch(ctx, s.url, s.latency)
			return err
		})
	}
	err := g.Wait()
	fmt.Println("Wait returned:", err)
	// The slow fetches were cancelled instead of running their full 500ms.
	fmt.Println("finished quickly (cancelled the slow ones):", time.Since(start) < 300*time.Millisecond)
}
