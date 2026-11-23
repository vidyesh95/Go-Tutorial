package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Handler unit test: no network, no ports. NewRecorder captures what the handler wrote.
func TestGreeter(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/hello?name=ada", nil)
	rec := httptest.NewRecorder()

	greeter{greeting: "Hi"}.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got, want := rec.Body.String(), "Hi, ada!\n"; got != want {
		t.Errorf("body = %q, want %q", got, want)
	}
}

func TestTeapotHeadersBeforeStatus(t *testing.T) {
	rec := httptest.NewRecorder()
	teapot(rec, httptest.NewRequest(http.MethodGet, "/teapot", nil))

	if rec.Code != http.StatusTeapot {
		t.Errorf("status = %d, want 418", rec.Code)
	}
	if got := rec.Header().Get("X-Brew"); got != "refused" {
		t.Errorf("X-Brew = %q, want refused", got)
	}
}

// Full-stack test over a real (in-memory) connection. NewTestServer closes
// itself when the test ends, so there is no `defer ts.Close()`.
func TestServerEndToEnd(t *testing.T) {
	ts := httptest.NewTestServer(t, newMux())
	client := ts.Client() // call Client() first: it also sets ts.URL

	resp, err := client.Post(ts.URL+"/echo", "text/plain", strings.NewReader("ping"))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if !strings.Contains(string(body), `body   : "ping"`) {
		t.Errorf("unexpected body: %s", body)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	rec := httptest.NewRecorder()
	newMux().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestServerHasTimeouts(t *testing.T) {
	srv := newServer(":0", newMux())
	if srv.ReadHeaderTimeout == 0 || srv.ReadTimeout == 0 || srv.WriteTimeout == 0 || srv.IdleTimeout == 0 {
		t.Error("server must set all four timeouts")
	}
}
