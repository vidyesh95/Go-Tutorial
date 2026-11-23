// Package gatewaysvc is the API gateway: the single public entry point in front of the
// internal services.
//
// WHAT A GATEWAY DOES (and why it exists): clients should not need to know how many services
// there are, where they live, or which port each uses. The gateway gives them ONE address and
// takes care of the cross-cutting concerns once, instead of in every service:
//   - routing   : /api/orders/* goes to orders-svc (path rewriting hides internal URLs);
//   - request ID: every request gets an X-Request-ID that flows through all services' logs;
//   - tracing   : starts/continues the distributed trace and forwards it downstream;
//   - timeouts  : a per-route deadline, so a hung service cannot hold client connections forever;
//   - health    : one /healthz that reports the state of everything behind it.
//
// Real gateways add authentication, rate limiting, TLS termination, CORS, caching and
// canary/blue-green routing. In industry you usually buy rather than build one (Envoy, Kong,
// NGINX, Traefik, AWS API Gateway, a Kubernetes Ingress); this package shows what they do.
//
// What a gateway must NOT do: contain business logic. The moment it "knows" what an order is,
// every feature change needs a gateway deploy and it becomes a distributed monolith's hub.
package gatewaysvc

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/svc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
)

// apiPrefix is stripped before forwarding: the public URL /api/orders/42 becomes /orders/42
// at the service. Clients see a versionable public surface; services keep simple internal paths.
const apiPrefix = "/api"

// Route sends /api/<Name>/... to Target.
type Route struct {
	Name    string        // path segment, e.g. "orders"
	Target  *url.URL      // base URL of the service, e.g. http://orders-svc:8081
	Timeout time.Duration // whole-request deadline for this route (default 5s)
}

// Config lists the routes the gateway serves.
type Config struct {
	Routes []Route
}

// New builds the gateway handler.
func New(cfg Config, log *slog.Logger, tp trace.TracerProvider) http.Handler {
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}
	// ONE transport shared by all routes: it owns the pool of keep-alive connections to the
	// services. otelhttp.NewTransport starts a client span per proxied request and injects the
	// `traceparent` header into it, which is how the trace continues into the service.
	// MaxIdleConnsPerHost defaults to 2, far too low for a proxy: under load it would keep
	// closing and re-opening connections to the same service.
	base := http.DefaultTransport.(*http.Transport).Clone()
	base.MaxIdleConnsPerHost = 100
	transport := otelhttp.NewTransport(base,
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(telemetry.Propagator()))

	mux := http.NewServeMux()
	for _, rt := range cfg.Routes {
		if rt.Timeout == 0 {
			rt.Timeout = 5 * time.Second
		}
		h := newProxy(rt, transport, log)
		base := apiPrefix + "/" + rt.Name
		mux.Handle(base, h)     // exact: POST /api/orders
		mux.Handle(base+"/", h) // subtree: /api/orders/{id}
	}
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		svc.WriteError(w, http.StatusNotFound, "no such route")
	})
	// /healthz = "how is the whole system?" (operators, dashboards). /livez = "is THIS process
	// alive?" (what Docker/Kubernetes should probe: restarting the gateway because orders-svc
	// is down would not help and would drop every in-flight request).
	mux.Handle("GET /healthz", healthHandler(cfg.Routes))
	mux.HandleFunc("GET /livez", func(w http.ResponseWriter, r *http.Request) {
		svc.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return otelhttp.NewHandler(svc.RequestID(svc.AccessLog(log, mux)), "gateway",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(telemetry.Propagator()))
}

// newProxy builds the reverse proxy for one route.
//
// ReverseProxy.Rewrite vs the old Director field: Director received the outgoing request but
// the proxy removed hop-by-hop headers AFTER calling it and added X-Forwarded-For itself, so
// Director could not reliably control forwarding headers, and a client could even name headers
// in `Connection:` to make the proxy strip ones Director had set. Director is deprecated
// (Go 1.26). Rewrite runs after the proxy has cleaned hop-by-hop and forwarding headers off the
// outgoing request, and gets BOTH the inbound request (read-only) and the outbound one.
func newProxy(rt Route, transport http.RoundTripper, log *slog.Logger) http.Handler {
	rp := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL.Path = strings.TrimPrefix(pr.In.URL.Path, apiPrefix)
			pr.Out.URL.RawPath = ""
			pr.SetURL(rt.Target) // scheme + host from the target; path is joined to the target's path
			// Set X-Forwarded-For/Host/Proto from the real client connection. Because Rewrite
			// starts from a CLEAN request, a client cannot spoof these headers to us.
			pr.SetXForwarded()
			// The RequestID middleware already stored the ID on the inbound request; copying it
			// explicitly documents that the gateway is where it is forwarded.
			pr.Out.Header.Set(svc.RequestIDHeader, pr.In.Header.Get(svc.RequestIDHeader))
		},
		Transport: transport,
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			id := r.Header.Get(svc.RequestIDHeader)
			if errors.Is(err, context.DeadlineExceeded) {
				log.Warn("upstream timed out", "route", rt.Name, "request_id", id)
				svc.WriteError(w, http.StatusGatewayTimeout, "upstream timed out") // 504
				return
			}
			log.Warn("upstream error", "route", rt.Name, "request_id", id, "err", err)
			svc.WriteError(w, http.StatusBadGateway, "upstream unavailable") // 502
		},
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Per-route timeout. The proxy sends the upstream request with r's context, so when
		// this deadline passes the upstream request is cancelled (the connection is closed and
		// the service sees its request context cancelled): the deadline propagates DOWN the chain.
		ctx, cancel := context.WithTimeout(r.Context(), rt.Timeout)
		defer cancel()
		rp.ServeHTTP(w, r.WithContext(ctx))
	})
}

// healthHandler aggregates the health of every downstream. It is a gateway-level readiness
// view for operators and dashboards: 200 if everything is up, 503 with the culprits otherwise.
func healthHandler(routes []Route) http.Handler {
	client := &http.Client{Timeout: 2 * time.Second}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		results := make([]string, len(routes))
		var wg sync.WaitGroup
		for i, rt := range routes {
			wg.Go(func() { // check all downstreams concurrently: total latency = the slowest, not the sum
				results[i] = probe(r.Context(), client, rt.Target)
			})
		}
		wg.Wait()

		down := make(map[string]string, len(routes))
		code, overall := http.StatusOK, "ok"
		for i, rt := range routes {
			down[rt.Name] = results[i]
			if results[i] != "ok" {
				code, overall = http.StatusServiceUnavailable, "degraded"
			}
		}
		svc.WriteJSON(w, code, map[string]any{"status": overall, "downstreams": down})
	})
}

func probe(ctx context.Context, c *http.Client, target *url.URL) string {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.JoinPath("healthz").String(), nil)
	if err != nil {
		return "error: " + err.Error()
	}
	resp, err := c.Do(req)
	if err != nil {
		return "error: unreachable"
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "error: status " + resp.Status
	}
	return "ok"
}

// Run serves the gateway on ln until ctx is cancelled, then drains in-flight requests.
func Run(ctx context.Context, ln net.Listener, cfg Config, log *slog.Logger, tp trace.TracerProvider) error {
	log.Info("gateway listening", "addr", ln.Addr().String())
	return svc.ServeHTTP(ctx, ln, New(cfg, log, tp), log)
}
