package main

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testConfig(buf *bytes.Buffer) appConfig {
	return appConfig{
		logger:         slog.New(slog.NewJSONHandler(buf, nil)),
		adminToken:     "tok",
		allowedOrigins: []string{"https://app.example.com"},
		slowTimeout:    50 * time.Millisecond,
	}
}

func serve(h http.Handler, method, target string, hdr map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestChainOrderFirstIsOutermost(t *testing.T) {
	var trace []string
	mw := func(name string) Middleware {
		return func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				trace = append(trace, "in:"+name)
				next.ServeHTTP(w, r)
				trace = append(trace, "out:"+name)
			})
		}
	}
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { trace = append(trace, "handler") }),
		mw("A"), mw("B"), mw("C"))
	serve(h, "GET", "/", nil)

	want := "in:A in:B in:C handler out:C out:B out:A"
	if got := strings.Join(trace, " "); got != want {
		t.Errorf("trace = %q, want %q", got, want)
	}
}

func TestRequestID(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(testConfig(&buf))

	generated := serve(h, "GET", "/hello", nil).Header().Get("X-Request-ID")
	if len(generated) != 36 {
		t.Errorf("generated id %q is not a UUID", generated)
	}
	if got := serve(h, "GET", "/hello", map[string]string{"X-Request-ID": "abc-123"}).Header().Get("X-Request-ID"); got != "abc-123" {
		t.Errorf("valid incoming id not reused: %q", got)
	}
	if got := serve(h, "GET", "/hello", map[string]string{"X-Request-ID": "bad id\nwith newline"}).Header().Get("X-Request-ID"); got == "bad id\nwith newline" {
		t.Error("unsafe incoming id was reused")
	}
}

func TestAccessLogRecordsStatusAndBytes(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(testConfig(&buf))
	rec := serve(h, "GET", "/hello", nil)

	var line struct {
		Method, Path  string
		Status, Bytes int
		RequestID     string `json:"request_id"`
	}
	if err := json.Unmarshal(buf.Bytes(), &line); err != nil {
		t.Fatalf("log line is not JSON: %v\n%s", err, buf.String())
	}
	if line.Method != "GET" || line.Path != "/hello" || line.Status != 200 || line.Bytes != rec.Body.Len() {
		t.Errorf("unexpected log line: %+v (body len %d)", line, rec.Body.Len())
	}
	if line.RequestID != rec.Header().Get("X-Request-ID") {
		t.Error("log request_id does not match response header")
	}
}

func TestResponseWriterUnwrapKeepsFlush(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := &responseWriter{ResponseWriter: rec}
	if err := http.NewResponseController(rw).Flush(); err != nil {
		t.Errorf("Flush through wrapper failed: %v (is Unwrap implemented?)", err)
	}
	if !rec.Flushed {
		t.Error("underlying recorder was not flushed")
	}
}

func TestRecovererReturnsJSON500AndLogs(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(testConfig(&buf))
	rec := serve(h, "GET", "/panic", nil)

	if rec.Code != 500 {
		t.Fatalf("status = %d", rec.Code)
	}
	var body struct{ Error struct{ Code string } }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.Error.Code != "internal" {
		t.Errorf("body = %q (%v)", rec.Body, err)
	}
	logs := buf.String()
	if !strings.Contains(logs, "panic recovered") || !strings.Contains(logs, `"status":500`) {
		t.Errorf("expected both the panic log and a 500 access log, got:\n%s", logs)
	}
}

func TestCORS(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(testConfig(&buf))

	pre := serve(h, "OPTIONS", "/orders", map[string]string{
		"Origin": "https://app.example.com", "Access-Control-Request-Method": "POST"})
	if pre.Code != 204 || pre.Header().Get("Access-Control-Allow-Origin") != "https://app.example.com" ||
		pre.Header().Get("Access-Control-Allow-Methods") == "" {
		t.Errorf("bad preflight: %d %v", pre.Code, pre.Header())
	}

	other := serve(h, "OPTIONS", "/orders", map[string]string{
		"Origin": "https://evil.example", "Access-Control-Request-Method": "POST"})
	if other.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Error("unlisted origin must not get Allow-Origin")
	}
}

func TestCSRFProtection(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(testConfig(&buf))

	tests := []struct {
		name string
		hdr  map[string]string
		want int
	}{
		{"no browser headers", nil, 201},
		{"same site", map[string]string{"Sec-Fetch-Site": "same-origin"}, 201},
		{"cross site", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, 403},
		{"trusted origin", map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://app.example.com"}, 201},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := serve(h, "POST", "/orders", tt.hdr).Code; got != tt.want {
				t.Errorf("status = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestBearerAuth(t *testing.T) {
	var buf bytes.Buffer
	h := newHandler(testConfig(&buf))

	if rec := serve(h, "GET", "/admin/stats", nil); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") == "" {
		t.Errorf("no token: %d %v", rec.Code, rec.Header())
	}
	if rec := serve(h, "GET", "/admin/stats", map[string]string{"Authorization": "Bearer wrong"}); rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	if rec := serve(h, "GET", "/admin/stats", map[string]string{"Authorization": "Bearer tok"}); rec.Code != 200 {
		t.Errorf("right token: %d", rec.Code)
	}
	// Public routes stay public.
	if rec := serve(h, "GET", "/hello", nil); rec.Code != 200 {
		t.Errorf("/hello = %d", rec.Code)
	}
}

func TestTimeoutHandler(t *testing.T) {
	var buf bytes.Buffer
	ts := httptest.NewTestServer(t, newHandler(testConfig(&buf)))
	client := ts.Client() // call Client() first: it also sets ts.URL
	resp, err := client.Get(ts.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", resp.StatusCode)
	}
}
