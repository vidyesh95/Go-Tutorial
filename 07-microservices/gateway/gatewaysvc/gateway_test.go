package gatewaysvc

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// backend is a fake orders-svc: it echoes what the gateway forwarded so the test can inspect it.
// httptest.NewServer listens on a real loopback port, which the gateway's own transport can reach.
func backend(t *testing.T, h http.HandlerFunc) *url.URL {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func echo(w http.ResponseWriter, r *http.Request) {
	json.NewEncoder(w).Encode(map[string]string{
		"method":     r.Method,
		"path":       r.URL.Path,
		"query":      r.URL.RawQuery,
		"request_id": r.Header.Get("X-Request-ID"),
		"idem_key":   r.Header.Get("Idempotency-Key"),
		"fwd_for":    r.Header.Get("X-Forwarded-For"),
	})
}

func gateway(t *testing.T, routes ...Route) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(New(Config{Routes: routes}, discard, nil))
	t.Cleanup(srv.Close)
	return srv
}

func do(t *testing.T, req *http.Request) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]any
	json.NewDecoder(resp.Body).Decode(&m) // non-JSON bodies leave m nil
	return resp, m
}

func get(t *testing.T, url string) (*http.Response, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	return do(t, req)
}

func TestProxyRewritesPathAndForwardsHeaders(t *testing.T) {
	gw := gateway(t, Route{Name: "orders", Target: backend(t, echo)})

	req, _ := http.NewRequest(http.MethodPost, gw.URL+"/api/orders?x=1", strings.NewReader("{}"))
	req.Header.Set("Idempotency-Key", "abc")
	resp, got := do(t, req)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got["path"] != "/orders" || got["query"] != "x=1" || got["method"] != "POST" {
		t.Errorf("backend saw %v; want POST /orders?x=1 (the /api prefix is stripped)", got)
	}
	if got["idem_key"] != "abc" {
		t.Errorf("Idempotency-Key not forwarded: %v", got)
	}
	if got["fwd_for"] == "" {
		t.Error("X-Forwarded-For not set")
	}

	_, got = get(t, gw.URL+"/api/orders/123")
	if got["path"] != "/orders/123" {
		t.Errorf("subtree path = %v", got["path"])
	}
}

func TestRequestIDGeneratedForwardedAndEchoed(t *testing.T) {
	gw := gateway(t, Route{Name: "orders", Target: backend(t, echo)})

	resp, got := get(t, gw.URL+"/api/orders/1")
	id := resp.Header.Get("X-Request-ID")
	if id == "" || got["request_id"] != id {
		t.Fatalf("generated id %q, backend saw %v: want the same non-empty value", id, got["request_id"])
	}

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/api/orders/1", nil)
	req.Header.Set("X-Request-ID", "client-chosen")
	resp, got = do(t, req)
	if resp.Header.Get("X-Request-ID") != "client-chosen" || got["request_id"] != "client-chosen" {
		t.Errorf("client-supplied request id was not preserved: %v", got["request_id"])
	}
}

func TestTraceContextIsPropagated(t *testing.T) {
	var traceparent string
	gw := gateway(t, Route{Name: "orders", Target: backend(t, func(w http.ResponseWriter, r *http.Request) {
		traceparent = r.Header.Get("traceparent")
	})})

	req, _ := http.NewRequest(http.MethodGet, gw.URL+"/api/orders/1", nil)
	// The gateway's tracer here is a no-op, which still passes the INCOMING context through.
	const incoming = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	req.Header.Set("traceparent", incoming)
	do(t, req)
	if !strings.HasPrefix(traceparent, "00-4bf92f3577b34da6a3ce929d0e0e4736-") {
		t.Fatalf("backend got traceparent %q; want it to continue trace 4bf92f35...", traceparent)
	}
}

func TestUnknownRoute(t *testing.T) {
	gw := gateway(t, Route{Name: "orders", Target: backend(t, echo)})
	resp, _ := get(t, gw.URL+"/api/nothing")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", resp.StatusCode)
	}
}

func TestRouteTimeoutReturns504(t *testing.T) {
	slow := backend(t, func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(5 * time.Second):
		case <-r.Context().Done(): // the gateway hung up: the deadline reached the backend
		}
	})
	gw := gateway(t, Route{Name: "orders", Target: slow, Timeout: 50 * time.Millisecond})
	resp, _ := get(t, gw.URL+"/api/orders/1")
	if resp.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", resp.StatusCode)
	}
}

func TestDeadBackendReturns502(t *testing.T) {
	dead := httptest.NewServer(nil)
	target, _ := url.Parse(dead.URL)
	dead.Close()
	gw := gateway(t, Route{Name: "orders", Target: target})
	resp, _ := get(t, gw.URL+"/api/orders/1")
	if resp.StatusCode != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502", resp.StatusCode)
	}
}

func TestHealthzAggregatesDownstreams(t *testing.T) {
	healthy := backend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusOK) })
	gw := gateway(t, Route{Name: "orders", Target: healthy})
	resp, got := get(t, gw.URL+"/healthz")
	if resp.StatusCode != http.StatusOK || got["status"] != "ok" {
		t.Fatalf("healthy: %d %v", resp.StatusCode, got)
	}

	unhealthy := backend(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) })
	dead := httptest.NewServer(nil)
	deadURL, _ := url.Parse(dead.URL)
	dead.Close()
	gw = gateway(t,
		Route{Name: "orders", Target: healthy},
		Route{Name: "billing", Target: unhealthy},
		Route{Name: "search", Target: deadURL})
	resp, got = get(t, gw.URL+"/healthz")
	if resp.StatusCode != http.StatusServiceUnavailable || got["status"] != "degraded" {
		t.Fatalf("degraded: %d %v", resp.StatusCode, got)
	}
	down := got["downstreams"].(map[string]any)
	if down["orders"] != "ok" || down["billing"] == "ok" || down["search"] == "ok" {
		t.Errorf("downstreams = %v", down)
	}

	// /livez does not depend on downstreams.
	if resp, _ := get(t, gw.URL+"/livez"); resp.StatusCode != http.StatusOK {
		t.Errorf("livez = %d", resp.StatusCode)
	}
}
