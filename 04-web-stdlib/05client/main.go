// Lesson 05: the HTTP client
//
// Rules for a production http.Client (every item is a real outage someone had):
//
//  1. NEVER use http.DefaultClient or http.Get for real work: no timeout, so one hung
//     server hangs your goroutine forever. Build a client with Timeout (whole
//     exchange: connect + redirects + reading the body).
//  2. Create ONE client and share it. It is safe for concurrent use and its
//     Transport holds the connection pool. A client per request throws the pool away
//     (new TCP + TLS handshake every time) and leaks idle connections.
//  3. Tune the Transport for your traffic. The default keeps only 2 idle connections
//     PER HOST (DefaultMaxIdleConnsPerHost). Call one backend from 50 goroutines and
//     48 connections are closed and re-opened constantly. Raise MaxIdleConnsPerHost.
//  4. Use http.NewRequestWithContext so the caller can cancel or set a deadline per
//     call (context chains from the incoming request in a server).
//  5. ALWAYS close resp.Body, and read it to EOF (or drain a bounded amount) first:
//     the connection goes back to the pool for reuse only if the body was fully read
//     and closed. Forgetting Close leaks the connection; closing without reading
//     usually closes the connection instead of reusing it. Section 1 of the demo
//     proves it by counting TCP connections.
//  6. A non-2xx response is NOT an error to net/http: err is nil for a 404 or 500. Check
//     resp.StatusCode yourself.
//  7. Bound what you read: io.LimitReader(resp.Body, max). A buggy server can send GBs.
//
// Retries (section 2 of the demo). Only retry when it is safe and likely to help:
//   - Safe = idempotent: GET, HEAD, OPTIONS, PUT, DELETE. Retrying a POST can create
//     the order twice. A POST becomes retryable if it carries an Idempotency-Key header
//     the server honours (Stripe-style).
//   - Likely to help = network errors, 429 Too Many Requests, 5xx (not 501). Never retry
//     other 4xx: the same bad request fails again.
//   - Exponential backoff (base, 2x base, 4x base ... capped) so a struggling server gets
//     breathing room, plus JITTER (randomness) so a thousand clients that failed together
//     do not all retry at the same instant (thundering herd).
//   - If the server says Retry-After (seconds or a date), respect it, but cap it: a
//     hostile or buggy "Retry-After: 86400" must not park your goroutine for a day.
//   - Limit attempts and stop as soon as the context is done.
//
// This lesson needs no -serve flag: both the server and the client run in this process.
// The "server" is a deliberately flaky httptest server.
//
// In industry: wrap this logic in one internal client package per upstream so timeouts,
// retries, metrics and tracing are consistent (often via a RoundTripper middleware, the
// client-side twin of lesson 04). Libraries like hashicorp/go-retryablehttp or
// sethvargo/go-retry exist, and the retry budget is paired with circuit breakers
// so retries cannot melt a server that is already down.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- client construction ----------

// newHTTPClient builds a tuned client. Cloning DefaultTransport keeps its sane
// defaults (proxy from env, dial timeouts, HTTP/2, ...) while we change what we need.
func newHTTPClient(timeout time.Duration) (*http.Client, *http.Transport) {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 100       // across all hosts
	tr.MaxIdleConnsPerHost = 20 // the important one for a service talking to a few backends
	tr.IdleConnTimeout = 90 * time.Second
	tr.ResponseHeaderTimeout = 5 * time.Second // server accepted the request but never answers
	return &http.Client{Transport: tr, Timeout: timeout}, tr
}

// drainClose lets the connection return to the pool. Bounded: if the leftover body is
// huge, closing the connection is cheaper than reading it.
func drainClose(body io.ReadCloser) {
	io.Copy(io.Discard, io.LimitReader(body, 4<<10))
	body.Close()
}

// ---------- retry policy ----------

type retryPolicy struct {
	MaxAttempts    int           // total tries including the first
	BaseDelay      time.Duration // first backoff
	MaxDelay       time.Duration // cap for both backoff and Retry-After
	AttemptTimeout time.Duration // per-attempt deadline (0 = none)

	wait    func(ctx context.Context, d time.Duration) error // injectable for tests
	onRetry func(attempt int, reason string, delay time.Duration)
}

// backoff returns "equal jitter": half fixed, half random. attempt starts at 0.
func (p retryPolicy) backoff(attempt int) time.Duration {
	d := min(p.MaxDelay, p.BaseDelay<<min(attempt, 20)) // clamp the shift so it cannot overflow
	if d < 2 {
		return d
	}
	return d/2 + rand.N(d/2)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func isIdempotent(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions, http.MethodPut, http.MethodDelete:
		return true
	}
	return req.Header.Get("Idempotency-Key") != ""
}

func retryableStatus(code int) bool {
	return code == http.StatusTooManyRequests || (code >= 500 && code != http.StatusNotImplemented)
}

// parseRetryAfter understands both forms: "120" (seconds) and an HTTP date.
func parseRetryAfter(v string, now time.Time) (time.Duration, bool) {
	if secs, err := strconv.Atoi(v); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second, true
	}
	if t, err := http.ParseTime(v); err == nil {
		return max(0, t.Sub(now)), true
	}
	return 0, false
}

// ---------- client wrapper ----------

type Client struct {
	hc    *http.Client
	retry retryPolicy
}

// StatusError is returned for non-2xx responses so callers can inspect the code.
type StatusError struct {
	Code int
	Body string // truncated snippet, useful in logs
}

func (e *StatusError) Error() string { return fmt.Sprintf("unexpected status %d: %s", e.Code, e.Body) }

// Do sends req, retrying when allowed. On success the returned response body is open
// and the caller must close it. The final response is returned even if its status
// is an error, so the caller can inspect it; intermediate responses are drained.
func (c *Client) Do(ctx context.Context, req *http.Request) (*http.Response, error) {
	p := c.retry
	if p.wait == nil {
		p.wait = sleepCtx
	}
	canRetry := isIdempotent(req)

	for attempt := 0; ; attempt++ {
		resp, err := c.attempt(ctx, req, p)
		last := attempt+1 >= p.MaxAttempts
		var delay time.Duration
		var reason string

		switch {
		case err != nil:
			// Context cancelled/expired by the CALLER: stop at once. (A per-attempt timeout
			// is also a DeadlineExceeded, so check the parent context, not the error.)
			if ctx.Err() != nil || !canRetry || last {
				return nil, err
			}
			reason = "network error: " + err.Error()
			delay = p.backoff(attempt)
		case !retryableStatus(resp.StatusCode) || !canRetry || last:
			return resp, nil
		default:
			reason = "status " + strconv.Itoa(resp.StatusCode)
			delay = p.backoff(attempt)
			if ra, ok := parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()); ok {
				delay = max(delay, min(ra, p.MaxDelay)) // honour the server, but capped
			}
			drainClose(resp.Body) // free the connection before sleeping
		}

		if p.onRetry != nil {
			p.onRetry(attempt+1, reason, delay)
		}
		if err := p.wait(ctx, delay); err != nil {
			return nil, err
		}
	}
}

// attempt runs one try with its own timeout. The body is re-created from GetBody
// (NewRequestWithContext fills it in for bytes.Reader/strings.Reader bodies).
func (c *Client) attempt(ctx context.Context, req *http.Request, p retryPolicy) (*http.Response, error) {
	actx, cancel := ctx, context.CancelFunc(func() {})
	if p.AttemptTimeout > 0 {
		actx, cancel = context.WithTimeout(ctx, p.AttemptTimeout)
	}
	r := req.Clone(actx)
	if req.GetBody != nil {
		body, err := req.GetBody()
		if err != nil {
			cancel()
			return nil, err
		}
		r.Body = body
	}
	resp, err := c.hc.Do(r)
	if err != nil {
		cancel()
		return nil, err
	}
	// Cancel the attempt's context only when the body is closed, otherwise reading
	// the body would fail with "context canceled".
	resp.Body = &cancelOnClose{ReadCloser: resp.Body, cancel: cancel}
	return resp, nil
}

type cancelOnClose struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelOnClose) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

// GetJSON: request -> status check -> bounded decode -> always drain+close.
func (c *Client) GetJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	return c.doJSON(ctx, req, out)
}

func (c *Client) PostJSON(ctx context.Context, url string, in, out any, idempotencyKey string) error {
	payload, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(payload)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotency-Key", idempotencyKey)
	}
	return c.doJSON(ctx, req, out)
}

func (c *Client) doJSON(ctx context.Context, req *http.Request, out any) error {
	resp, err := c.Do(ctx, req)
	if err != nil {
		return err
	}
	defer drainClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// ---------- flaky demo server ----------

// failTimes answers `status` the first n calls, then `okStatus` with a JSON body.
func failTimes(n int, status int, retryAfter string, okStatus int) http.HandlerFunc {
	var calls atomic.Int32
	return func(w http.ResponseWriter, r *http.Request) {
		if int(calls.Add(1)) <= n {
			if retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
			http.Error(w, "try again later", status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(okStatus)
		fmt.Fprintf(w, `{"id":1,"name":"Ada","served_after_calls":%d}`+"\n", calls.Load())
	}
}

func newFlakyHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ok", failTimes(0, 0, "", 200))
	mux.HandleFunc("GET /flaky", failTimes(2, 503, "", 200))         // recovers on 3rd call
	mux.HandleFunc("GET /ratelimited", failTimes(1, 429, "1", 200))  // asks for 1s
	mux.HandleFunc("GET /always500", failTimes(1<<30, 500, "", 200)) // never recovers
	mux.HandleFunc("GET /missing", failTimes(1<<30, 404, "", 200))   // 4xx: not retryable
	mux.HandleFunc("POST /orders-nokey", failTimes(1, 503, "", 201))
	mux.HandleFunc("POST /orders-key", failTimes(1, 503, "", 201))
	mux.HandleFunc("GET /hang", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // never answer; return when the client gives up
	})
	mux.HandleFunc("GET /big", func(w http.ResponseWriter, r *http.Request) {
		w.Write(make([]byte, 4<<20)) // far bigger than any buffer: the client cannot get it all without really reading
	})
	return mux
}

// ---------- demo ----------

type user struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	connectionReuseDemo()
	retryDemo()
}

// connectionReuseDemo counts TCP connections the server accepted.
func connectionReuseDemo() {
	fmt.Println("=== 1. connection reuse: drain+close vs close only")

	var conns atomic.Int32
	ts := httptest.NewUnstartedServer(newFlakyHandler())
	ts.Config.ConnState = func(_ net.Conn, s http.ConnState) {
		if s == http.StateNew {
			conns.Add(1)
		}
	}
	ts.Start()
	defer ts.Close()

	run := func(label string, read bool) {
		conns.Store(0)
		hc, tr := newHTTPClient(2 * time.Second)
		defer tr.CloseIdleConnections()
		for range 5 {
			resp, err := hc.Get(ts.URL + "/big")
			if err != nil {
				log.Fatal(err)
			}
			if read {
				io.Copy(io.Discard, resp.Body) // read to EOF ...
			}
			resp.Body.Close() // ... then close
		}
		fmt.Printf("%-28s -> 5 requests used %d TCP connection(s)\n", label, conns.Load())
	}
	run("read to EOF, then Close", true)
	run("Close without reading", false)
}

func retryDemo() {
	fmt.Println("\n=== 2. retries against a flaky server")
	ts := httptest.NewServer(newFlakyHandler())
	defer ts.Close()

	hc, tr := newHTTPClient(2 * time.Second)
	defer tr.CloseIdleConnections()

	var mu sync.Mutex
	var lines []string
	c := &Client{hc: hc, retry: retryPolicy{
		MaxAttempts:    4,
		BaseDelay:      20 * time.Millisecond,
		MaxDelay:       200 * time.Millisecond,
		AttemptTimeout: 150 * time.Millisecond,
		onRetry: func(attempt int, reason string, delay time.Duration) {
			mu.Lock()
			defer mu.Unlock()
			lines = append(lines, fmt.Sprintf("    attempt %d failed (%s); waiting %v", attempt, reason, delay.Round(time.Millisecond)))
		},
	}}

	scenario := func(label string, f func(ctx context.Context) error) {
		lines = nil
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := f(ctx)
		fmt.Printf("\n%s\n", label)
		for _, l := range lines {
			fmt.Println(l)
		}
		var se *StatusError
		switch {
		case err == nil:
			fmt.Println("    => success")
		case errors.As(err, &se):
			fmt.Printf("    => failed with HTTP status %d\n", se.Code)
		case errors.Is(err, context.DeadlineExceeded):
			fmt.Println("    => failed: timeout (context deadline exceeded)")
		default:
			fmt.Println("    => failed:", err)
		}
	}

	var u user
	scenario("GET /ok (no retry needed)", func(ctx context.Context) error {
		return c.GetJSON(ctx, ts.URL+"/ok", &u)
	})
	fmt.Printf("    decoded: %+v\n", u)
	scenario("GET /flaky (two 503s, then OK)", func(ctx context.Context) error {
		return c.GetJSON(ctx, ts.URL+"/flaky", &u)
	})
	scenario("GET /ratelimited (429 + Retry-After: 1, capped to MaxDelay)", func(ctx context.Context) error {
		return c.GetJSON(ctx, ts.URL+"/ratelimited", &u)
	})
	scenario("GET /missing (404 is never retried)", func(ctx context.Context) error {
		return c.GetJSON(ctx, ts.URL+"/missing", &u)
	})
	scenario("GET /always500 (gives up after MaxAttempts)", func(ctx context.Context) error {
		return c.GetJSON(ctx, ts.URL+"/always500", &u)
	})
	scenario("POST without Idempotency-Key (not retried: might double-create)", func(ctx context.Context) error {
		return c.PostJSON(ctx, ts.URL+"/orders-nokey", map[string]string{"item": "book"}, &u, "")
	})
	scenario("POST with Idempotency-Key (safe to retry)", func(ctx context.Context) error {
		return c.PostJSON(ctx, ts.URL+"/orders-key", map[string]string{"item": "book"}, &u, "order-7f3a")
	})
	c2 := &Client{hc: hc, retry: c.retry}
	c2.retry.MaxAttempts = 2
	scenario("GET /hang (per-attempt timeout 150ms, 2 attempts)", func(ctx context.Context) error {
		return c2.GetJSON(ctx, ts.URL+"/hang", &u)
	})
}
