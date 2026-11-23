// Use case 04: a concurrent, polite web crawler.
//
// Crawling is the textbook "I/O-bound fan-out" problem: thousands of independent HTTP fetches
// where each one mostly WAITS. Goroutines make that cheap, but unbounded concurrency would
// hammer the target site and exhaust your sockets. A production crawler needs limits.
//
// What this program demonstrates, and why:
//   - Bounded workers: errgroup.Group with SetLimit(n) allows at most n fetches at once.
//   - Level-by-level (breadth-first) crawling. WHY NOT recursion + errgroup? If a fetch
//     goroutine calls g.Go for each child while all n slots are taken by parents, the parents
//     wait for slots that only they can free: DEADLOCK. Processing one depth level at a time
//     (fetch the whole frontier, collect the next one, repeat) has no such cycle.
//   - Visited set guarded by a sync.Mutex: many workers discover the same link at once and
//     exactly one may claim it ("check then insert" must be one atomic step under the lock).
//   - Max depth, same-host only, a response-size cap (io.LimitReader) and content-type check,
//     so we never wander across the internet, read a 4GB file or parse a PNG as HTML.
//   - Per-request timeout via context.WithTimeout: a hung server costs one slot for a bounded time.
//   - Politeness: a golang.org/x/time/rate token-bucket limiter shared by all workers
//     (limiter.Wait(ctx) blocks until a token is free), an identifying User-Agent, and a
//     robots.txt check (simplified: only "User-agent: *" + "Disallow:" prefixes).
//   - Errors are DATA, not control flow: one bad page is recorded and the crawl continues.
//     Workers return nil to errgroup; only context cancellation stops the whole crawl.
//
// Link extraction uses golang.org/x/net/html, a real HTML5 tokenizer/parser. Do not parse HTML
// with regexp: it breaks on comments, scripts, odd quoting and malformed markup. (go.mod lists
// x/net as an indirect dependency; `go mod tidy` will mark it direct.)
//
// Try it:
//
//	go run ./08-use-cases/04crawler                                   # demo against an in-process site
//	go run ./08-use-cases/04crawler -url https://go.dev -depth 1 -rate 2 -workers 3
//
// Only crawl sites you own or have permission to crawl, keep the rate low, and obey robots.txt.
//
// In industry: Go powers crawlers, scrapers and link checkers (Colly is the popular framework),
// plus the same pattern in any "scan many things concurrently" job: port scanners, uptime
// probes, sitemap generators, cache warmers, API sync jobs and Prometheus-style scrapers that
// poll thousands of targets. Real crawlers add a persistent queue (so they can resume),
// per-host rate limits, retries with backoff, URL normalisation, and distributed workers.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/sync/errgroup"
	"golang.org/x/time/rate"
)

// Config holds the crawl policy.
type Config struct {
	Start    string
	MaxDepth int           // 0 = only the start page
	Workers  int           // max concurrent fetches
	Rate     float64       // requests per second across all workers
	Timeout  time.Duration // per request
	Client   *http.Client  // injectable for tests
}

// Page is the outcome for one URL.
type Page struct {
	URL    string
	Depth  int
	Parent string // the page on which this URL was first discovered ("" for the start page)
	Status int    // HTTP status, 0 if the request never completed
	Note   string // why it was skipped or failed
	Links  int    // number of links found on it
}

// Stats summarises a crawl.
type Stats struct {
	Fetched       int // pages downloaded and parsed
	HTTPErrors    int // non-2xx responses
	Failed        int // timeouts, connection errors
	RobotsSkipped int
	NonHTML       int
	External      int // links to other hosts (not followed)
	DepthLimited  int // new links found on pages at max depth (not followed)
	MaxInFlight   int // highest observed concurrency (must be <= Workers)
	Elapsed       time.Duration
}

// Crawler holds shared state. Everything mutable is guarded by mu.
type Crawler struct {
	cfg     Config
	base    *url.URL
	limiter *rate.Limiter
	robots  []string // disallowed path prefixes

	mu       sync.Mutex
	seen     map[string]bool // URLs already claimed (queued, fetched or skipped)
	pages    map[string]*Page
	stats    Stats
	inFlight int
}

func NewCrawler(cfg Config) (*Crawler, error) {
	base, err := url.Parse(cfg.Start)
	if err != nil || base.Host == "" {
		return nil, fmt.Errorf("bad start URL %q", cfg.Start)
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{}
	}
	cfg.Workers = max(cfg.Workers, 1)
	burst := max(int(cfg.Rate), 1)
	return &Crawler{
		cfg:     cfg,
		base:    base,
		limiter: rate.NewLimiter(rate.Limit(cfg.Rate), burst),
		seen:    make(map[string]bool),
		pages:   make(map[string]*Page),
	}, nil
}

// normalize resolves href against the page URL, drops the #fragment (same document) and
// returns "" for things we cannot crawl (mailto:, javascript:, bad URLs).
func normalize(pageURL *url.URL, href string) string {
	u, err := pageURL.Parse(href) // Parse = resolve relative to pageURL
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return ""
	}
	u.Fragment = ""
	return u.String()
}

// claim atomically marks u as seen and reports whether the caller won. This is the critical
// section: two workers finding the same link must not both fetch it.
func (c *Crawler) claim(u string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.seen[u] {
		return false
	}
	c.seen[u] = true
	return true
}

func (c *Crawler) record(p *Page) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.pages[p.URL] = p
}

func (c *Crawler) bump(f func(s *Stats)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	f(&c.stats)
}

// Crawl runs the whole crawl and returns pages sorted by URL plus stats.
func (c *Crawler) Crawl(ctx context.Context) ([]*Page, Stats, error) {
	started := time.Now()
	c.loadRobots(ctx)

	startURL := normalize(c.base, c.cfg.Start)
	c.claim(startURL)
	frontier := []*Page{{URL: startURL}}

	for depth := 0; len(frontier) > 0 && depth <= c.cfg.MaxDepth; depth++ {
		var next []*Page
		var nextMu sync.Mutex

		g, gctx := errgroup.WithContext(ctx)
		g.SetLimit(c.cfg.Workers) // g.Go blocks while Workers goroutines are running
		for _, p := range frontier {
			p.Depth = depth
			g.Go(func() error {
				children := c.visit(gctx, p)
				nextMu.Lock()
				next = append(next, children...)
				nextMu.Unlock()
				return nil // page-level failures are recorded in Page, they never abort the crawl
			})
		}
		if err := g.Wait(); err != nil {
			return nil, c.stats, err
		}
		if err := ctx.Err(); err != nil {
			return nil, c.stats, err
		}
		// Sort so the frontier (and therefore the output) does not depend on goroutine timing.
		slices.SortFunc(next, func(a, b *Page) int { return strings.Compare(a.URL, b.URL) })
		frontier = next
	}

	c.stats.Elapsed = time.Since(started)
	out := make([]*Page, 0, len(c.pages))
	for _, p := range c.pages {
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b *Page) int { return strings.Compare(a.URL, b.URL) })
	return out, c.stats, nil
}

// visit fetches one page, records the outcome and returns newly discovered same-host pages.
func (c *Crawler) visit(ctx context.Context, p *Page) []*Page {
	defer c.record(p)
	u, _ := url.Parse(p.URL)

	if !c.allowed(u.Path) {
		p.Note = "skipped: disallowed by robots.txt"
		c.bump(func(s *Stats) { s.RobotsSkipped++ })
		return nil
	}

	// Politeness: wait for a token. This is where the global request rate is enforced.
	if err := c.limiter.Wait(ctx); err != nil {
		p.Note = "cancelled"
		return nil
	}

	c.bump(func(s *Stats) {
		c.inFlight++
		s.MaxInFlight = max(s.MaxInFlight, c.inFlight)
	})
	defer c.bump(func(*Stats) { c.inFlight-- })

	// Per-request timeout. The deadline covers connect, headers AND reading the body.
	rctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, http.MethodGet, p.URL, nil)
	req.Header.Set("User-Agent", "go-tutorial-crawler/1.0 (learning project)")
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		p.Note = "failed: " + shortErr(err)
		c.bump(func(s *Stats) { s.Failed++ })
		return nil
	}
	defer resp.Body.Close()
	p.Status = resp.StatusCode

	if resp.StatusCode/100 != 2 {
		p.Note = "http error"
		c.bump(func(s *Stats) { s.HTTPErrors++ })
		return nil
	}
	if mt, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type")); mt != "text/html" {
		p.Note = "skipped: " + mt
		c.bump(func(s *Stats) { s.NonHTML++ })
		return nil
	}

	links, err := extractLinks(io.LimitReader(resp.Body, 1<<20)) // never read more than 1 MiB
	if err != nil {
		p.Note = "failed: " + shortErr(err)
		c.bump(func(s *Stats) { s.Failed++ })
		return nil
	}
	p.Links = len(links)
	c.bump(func(s *Stats) { s.Fetched++ })

	var children []*Page
	for _, href := range links {
		target := normalize(u, href)
		if target == "" {
			continue
		}
		tu, _ := url.Parse(target)
		if tu.Host != c.base.Host { // same-host only: never crawl the open internet
			if c.claim(target) {
				c.bump(func(s *Stats) { s.External++ })
			}
			continue
		}
		if !c.claim(target) {
			continue // somebody already queued/visited it (also breaks cycles)
		}
		if p.Depth >= c.cfg.MaxDepth {
			c.bump(func(s *Stats) { s.DepthLimited++ })
			continue
		}
		children = append(children, &Page{URL: target, Parent: p.URL})
	}
	return children
}

// extractLinks returns every <a href> value. It uses the real HTML parser, which copes with
// the messy markup found in the wild. Node.Descendants (x/net >= 0.30) is an iterator over
// the whole tree, so no recursion is needed.
func extractLinks(r io.Reader) ([]string, error) {
	doc, err := html.Parse(r)
	if err != nil {
		return nil, err
	}
	var links []string
	for n := range doc.Descendants() {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, a := range n.Attr {
				if a.Key == "href" {
					links = append(links, a.Val)
				}
			}
		}
	}
	return links, nil
}

// loadRobots fetches /robots.txt and keeps the Disallow prefixes under "User-agent: *".
// Real parsers (e.g. temoto/robotstxt) also handle Allow, wildcards, Crawl-delay and
// per-bot groups. A missing or unreadable file means "everything allowed".
func (c *Crawler) loadRobots(ctx context.Context) {
	u := *c.base
	u.Path, u.RawQuery, u.Fragment = "/robots.txt", "", ""
	rctx, cancel := context.WithTimeout(ctx, c.cfg.Timeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(rctx, http.MethodGet, u.String(), nil)
	resp, err := c.cfg.Client.Do(req)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return
	}
	c.robots = parseRobots(resp.Body)
}

func parseRobots(r io.Reader) []string {
	var disallowed []string
	applies := false
	sc := bufio.NewScanner(io.LimitReader(r, 512<<10))
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#") // strip comments
		key, val, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		key, val = strings.ToLower(strings.TrimSpace(key)), strings.TrimSpace(val)
		switch key {
		case "user-agent":
			applies = val == "*"
		case "disallow":
			if applies && val != "" {
				disallowed = append(disallowed, val)
			}
		}
	}
	return disallowed
}

func (c *Crawler) allowed(path string) bool {
	return !slices.ContainsFunc(c.robots, func(prefix string) bool { return strings.HasPrefix(path, prefix) })
}

func shortErr(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	s := err.Error()
	if i := strings.LastIndex(s, ": "); i >= 0 {
		s = s[i+2:] // keep the innermost cause, e.g. "connection refused"
	}
	return s
}

// PrintTree prints pages as a tree following "discovered on" parent links.
func PrintTree(w io.Writer, pages []*Page, root string) {
	children := make(map[string][]*Page)
	var rootPage *Page
	for _, p := range pages {
		if p.URL == root {
			rootPage = p
			continue
		}
		children[p.Parent] = append(children[p.Parent], p)
	}
	var walk func(p *Page, indent string)
	walk = func(p *Page, indent string) {
		u, _ := url.Parse(p.URL)
		label := u.RequestURI()
		detail := fmt.Sprintf("[%d]", p.Status)
		if p.Status == 0 {
			detail = "[---]"
		}
		if p.Note != "" {
			detail += " " + p.Note
		} else {
			detail += fmt.Sprintf(" %d links", p.Links)
		}
		fmt.Fprintf(w, "%s%s  %s\n", indent, label, detail)
		for _, ch := range children[p.URL] {
			walk(ch, indent+"    ")
		}
	}
	if rootPage != nil {
		walk(rootPage, "")
	}
}

// ---------------------------------------------------------------------------------------
// Demo site: ~15 linked pages served in-process, with every kind of trouble a crawler meets.

func demoSite() http.Handler {
	// path -> hrefs on that page
	site := map[string][]string{
		"/":                       {"/docs/", "/blog/", "/about", "/files/logo.png", "/private/admin", "/slow", "/gone", "https://other.example/partner", "mailto:hi@example.com"},
		"/docs/":                  {"/docs/intro", "/docs/api", "/docs/faq", "/", "#top"}, // links back to / : a cycle
		"/docs/intro":             {"/docs/api", "/docs/intro/deep"},
		"/docs/api":               {"/docs/api/v1", "/docs/api/v2", "/docs/intro"},
		"/docs/api/v1":            {"/docs/api/v2"},
		"/docs/api/v2":            {},
		"/docs/intro/deep":        {"/docs/intro/deep/deeper"},
		"/docs/intro/deep/deeper": {},
		"/blog/":                  {"/blog/archive", "/blog/post-1", "/blog/post-2", "/blog/post-3", "/"},
		"/blog/post-1":            {"/blog/post-2", "/about"},
		"/blog/post-2":            {"/blog/post-3"},
		"/blog/post-3":            {},
		"/docs/faq":               {"/about"},
		"/blog/archive":           {"/blog/post-1"},
		"/about":                  {"/", "/blog/"},
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "# be nice\nUser-agent: *\nDisallow: /private/\n")
	})
	mux.HandleFunc("GET /files/logo.png", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		w.Write([]byte("\x89PNG not really"))
	})
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select { // hangs far longer than the crawler's timeout; stops when the client gives up
		case <-time.After(5 * time.Second):
		case <-r.Context().Done():
		}
	})
	// "/gone" and "/private/admin" are linked but not registered here: /gone is a 404.
	mux.HandleFunc("GET /{path...}", func(w http.ResponseWriter, r *http.Request) {
		links, ok := site[r.URL.Path]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, "<!doctype html><html><head><title>%s</title></head><body><h1>%s</h1>\n", r.URL.Path, r.URL.Path)
		fmt.Fprint(w, "<!-- <a href=\"/commented-out\">not a real link</a> -->\n")
		for _, l := range links {
			fmt.Fprintf(w, "<p><a href=%q>%s</a></p>\n", l, l)
		}
		fmt.Fprint(w, "</body></html>")
	})
	return mux
}

func runDemo() error {
	ts := httptest.NewServer(demoSite())
	defer ts.Close()
	fmt.Println("demo site:", ts.URL, "(15 HTML pages plus robots.txt, a PNG, a hanging page, a 404 and external links)")

	cfg := Config{Start: ts.URL + "/", MaxDepth: 3, Workers: 4, Rate: 200, Timeout: 300 * time.Millisecond}
	fmt.Printf("policy: depth<=%d workers=%d rate=%.0f/s timeout=%v same-host only\n\n", cfg.MaxDepth, cfg.Workers, cfg.Rate, cfg.Timeout)
	return crawlAndReport(context.Background(), cfg)
}

func crawlAndReport(ctx context.Context, cfg Config) error {
	c, err := NewCrawler(cfg)
	if err != nil {
		return err
	}
	pages, stats, err := c.Crawl(ctx)
	if err != nil {
		return err
	}
	PrintTree(os.Stdout, pages, c.base.String())
	fmt.Println("\nstats:")
	fmt.Printf("  fetched ok:        %d\n", stats.Fetched)
	fmt.Printf("  http errors:       %d\n", stats.HTTPErrors)
	fmt.Printf("  timeouts/failures: %d\n", stats.Failed)
	fmt.Printf("  robots skipped:    %d\n", stats.RobotsSkipped)
	fmt.Printf("  non-HTML skipped:  %d\n", stats.NonHTML)
	fmt.Printf("  external links:    %d (not followed)\n", stats.External)
	fmt.Printf("  beyond max depth:  %d (not followed)\n", stats.DepthLimited)
	fmt.Printf("  max in flight:     %d (limit %d)\n", stats.MaxInFlight, cfg.Workers)
	fmt.Printf("  elapsed:           %v\n", stats.Elapsed.Round(10*time.Millisecond))
	return nil
}

func main() {
	target := flag.String("url", "", "start URL to crawl for real (default: run the in-process demo)")
	depth := flag.Int("depth", 2, "max link depth (with -url)")
	workers := flag.Int("workers", 4, "concurrent fetches (with -url)")
	rps := flag.Float64("rate", 2, "requests per second (with -url); keep it polite")
	timeout := flag.Duration("timeout", 5*time.Second, "per-request timeout (with -url)")
	flag.Parse()

	var err error
	if *target == "" {
		err = runDemo()
	} else {
		err = crawlAndReport(context.Background(), Config{Start: *target, MaxDepth: *depth, Workers: *workers, Rate: *rps, Timeout: *timeout})
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "crawl failed:", err)
		os.Exit(1)
	}
}
