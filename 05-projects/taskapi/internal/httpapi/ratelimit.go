package httpapi

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// ipLimiter enforces a token-bucket rate limit per client IP.
//
// x/time/rate.Limiter is one bucket: it refills at `rps` tokens/second up to `burst`, and each
// request takes one token. Keeping a map of buckets keyed by IP gives every client its own
// budget, so one noisy client cannot starve the others.
//
// The map grows with the number of distinct IPs, so a janitor goroutine evicts entries that have
// been idle for a while. Evicting an idle IP is lossless: a fresh bucket starts full, and an
// idle one would have refilled to full anyway.
//
// Limits: this is per-process memory. With several replicas each enforces its own limit (so the
// effective limit is N times higher); a shared limit needs Redis or an API gateway.
type ipLimiter struct {
	rps   rate.Limit
	burst int
	now   func() time.Time

	mu      sync.Mutex
	clients map[string]*clientEntry

	// done is closed when the janitor goroutine has exited. Tests wait on it to prove there is
	// no goroutine leak after the context is cancelled.
	done chan struct{}
}

type clientEntry struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

const (
	limiterCleanupInterval = time.Minute
	limiterIdleTTL         = 3 * time.Minute
)

// newIPLimiter creates the limiter and starts its janitor, which stops when ctx is cancelled.
func newIPLimiter(ctx context.Context, rps float64, burst int) *ipLimiter {
	l := &ipLimiter{
		rps: rate.Limit(rps), burst: burst, now: time.Now,
		clients: make(map[string]*clientEntry),
		done:    make(chan struct{}),
	}
	go l.janitor(ctx, limiterCleanupInterval)
	return l
}

func (l *ipLimiter) janitor(ctx context.Context, every time.Duration) {
	defer close(l.done)
	ticker := time.NewTicker(every)
	defer ticker.Stop() // release the ticker's resources when we exit
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			l.evictIdle(limiterIdleTTL)
		}
	}
}

// evictIdle removes entries not seen for at least ttl and returns how many were removed.
func (l *ipLimiter) evictIdle(ttl time.Duration) int {
	cutoff := l.now().Add(-ttl)
	l.mu.Lock()
	defer l.mu.Unlock()
	removed := 0
	for ip, c := range l.clients {
		if c.lastSeen.Before(cutoff) {
			delete(l.clients, ip) // deleting during range is safe in Go
			removed++
		}
	}
	return removed
}

// allow reports whether a request from ip may proceed, consuming one token if so.
func (l *ipLimiter) allow(ip string) bool {
	l.mu.Lock()
	c, ok := l.clients[ip]
	if !ok {
		c = &clientEntry{limiter: rate.NewLimiter(l.rps, l.burst)}
		l.clients[ip] = c
	}
	c.lastSeen = l.now()
	l.mu.Unlock()
	// Limiter is itself goroutine-safe, so the (slower) Allow call happens outside our lock.
	return c.limiter.Allow()
}

func (l *ipLimiter) size() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.clients)
}

// rateLimit rejects requests over the per-IP budget with 429 and a Retry-After header.
func rateLimit(l *ipLimiter, log *slog.Logger) middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !l.allow(clientIP(r)) {
				writeError(w, r, log, errRateLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

// clientIP returns the TCP peer address, deliberately ignoring X-Forwarded-For / X-Real-IP.
//
// Those headers are plain text that ANY client can set. If we trusted them, an attacker would
// send a different fake X-Forwarded-For on every request and get a fresh rate-limit bucket each
// time, bypassing the limiter completely (and could also frame other IPs in the logs).
//
// The catch: behind a reverse proxy or load balancer, RemoteAddr is the PROXY's address, so
// every user shares one bucket. The correct fix is to trust the header only when the direct peer
// is a proxy you control (configured allow-list of proxy CIDRs) and then take the right-most
// untrusted entry. That needs deployment knowledge, so it is left as an explicit next step.
func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr // already a bare host (some test setups)
	}
	return host
}
