package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// countingSite serves a fully-connected graph (every page links to every other page, so the
// crawler meets each URL many times) and records hits and peak concurrency per server.
type countingSite struct {
	mu       sync.Mutex
	hits     map[string]int
	inFlight atomic.Int32
	peak     atomic.Int32
}

func (s *countingSite) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	cur := s.inFlight.Add(1)
	defer s.inFlight.Add(-1)
	for { // record the peak without a lock
		p := s.peak.Load()
		if cur <= p || s.peak.CompareAndSwap(p, cur) {
			break
		}
	}
	s.mu.Lock()
	s.hits[r.URL.Path]++
	s.mu.Unlock()

	time.Sleep(10 * time.Millisecond) // keep requests overlapping so concurrency is observable
	if r.URL.Path == "/robots.txt" {
		fmt.Fprint(w, "User-agent: *\nDisallow: /secret\n")
		return
	}
	w.Header().Set("Content-Type", "text/html")
	var b strings.Builder
	for i := range 8 {
		fmt.Fprintf(&b, `<a href="/p%d">p%d</a> `, i, i)
	}
	b.WriteString(`<a href="/secret">secret</a> <a href="/p0#frag">frag</a>`)
	fmt.Fprint(w, b.String())
}

func TestEachURLFetchedOnceAndConcurrencyBounded(t *testing.T) {
	site := &countingSite{hits: map[string]int{}}
	ts := httptest.NewServer(site)
	defer ts.Close()

	const workers = 3
	c, err := NewCrawler(Config{Start: ts.URL + "/p0", MaxDepth: 3, Workers: workers, Rate: 1000, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	pages, stats, err := c.Crawl(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	// 8 pages + /secret (robots) are recorded. Despite the dense link graph, every page is
	// fetched exactly once: that is what the mutex-guarded visited set guarantees.
	if len(pages) != 9 {
		t.Errorf("recorded %d pages, want 9", len(pages))
	}
	for path, n := range site.hits {
		if path != "/robots.txt" && n != 1 {
			t.Errorf("%s fetched %d times, want exactly 1", path, n)
		}
	}
	if site.hits["/secret"] != 0 {
		t.Errorf("robots.txt-disallowed /secret was fetched")
	}
	if stats.RobotsSkipped != 1 {
		t.Errorf("RobotsSkipped = %d, want 1", stats.RobotsSkipped)
	}
	if got := site.peak.Load(); got > workers {
		t.Errorf("server saw %d concurrent requests, limit is %d", got, workers)
	}
	if stats.MaxInFlight > workers {
		t.Errorf("MaxInFlight = %d, limit is %d", stats.MaxInFlight, workers)
	}
}

func TestDepthLimit(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		var n int
		fmt.Sscanf(r.URL.Path, "/%d", &n)
		fmt.Fprintf(w, `<a href="/%d">next</a>`, n+1) // an endless chain /0 -> /1 -> /2 ...
	}))
	defer ts.Close()

	c, _ := NewCrawler(Config{Start: ts.URL + "/0", MaxDepth: 2, Workers: 2, Rate: 1000, Timeout: time.Second})
	pages, stats, err := c.Crawl(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 3 { // /0, /1, /2
		t.Errorf("got %d pages, want 3", len(pages))
	}
	if stats.DepthLimited != 1 {
		t.Errorf("DepthLimited = %d, want 1", stats.DepthLimited)
	}
}

func TestRequestTimeoutDoesNotAbortCrawl(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/hang" {
			<-r.Context().Done() // returns when the crawler gives up
			return
		}
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/hang">hang</a> <a href="/ok">ok</a>`)
	}))
	defer ts.Close()

	c, _ := NewCrawler(Config{Start: ts.URL + "/", MaxDepth: 1, Workers: 2, Rate: 1000, Timeout: 100 * time.Millisecond})
	_, stats, err := c.Crawl(context.Background())
	if err != nil {
		t.Fatalf("a hung page must not fail the crawl: %v", err)
	}
	if stats.Failed != 1 || stats.Fetched != 2 { // "/" and "/ok" fetched, "/hang" failed
		t.Errorf("stats = %+v, want Failed=1 Fetched=2", stats)
	}
}

func TestContextCancellationStopsCrawl(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/a">a</a>`)
	}))
	defer ts.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c, _ := NewCrawler(Config{Start: ts.URL + "/", MaxDepth: 5, Workers: 2, Rate: 1, Timeout: time.Second})
	if _, _, err := c.Crawl(ctx); err == nil {
		t.Error("expected an error from a cancelled context")
	}
}

func TestParseRobots(t *testing.T) {
	got := parseRobots(strings.NewReader("User-agent: googlebot\nDisallow: /g\n\nUser-agent: *\nDisallow: /private # comment\nDisallow:\n"))
	if len(got) != 1 || got[0] != "/private" {
		t.Errorf("parseRobots = %q, want [/private]", got)
	}
}
