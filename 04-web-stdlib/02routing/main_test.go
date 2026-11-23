package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Table-driven routing tests: the mux is a plain Handler, so no server is needed.
func TestRouting(t *testing.T) {
	h := newHandler()
	tests := []struct {
		name, method, target, host string
		wantCode                   int
		wantBody                   string // substring
		wantAllow                  string
	}{
		{name: "root", method: "GET", target: "/", wantCode: 200, wantBody: "home"},
		{name: "root exact only", method: "GET", target: "/zzz", wantCode: 404},
		{name: "wildcard", method: "GET", target: "/tasks/42", wantCode: 200, wantBody: "task 42"},
		{name: "literal beats wildcard", method: "GET", target: "/tasks/latest", wantCode: 200, wantBody: "latest task"},
		{name: "bad id", method: "GET", target: "/tasks/x", wantCode: 400},
		{name: "405 has Allow", method: "DELETE", target: "/tasks/42", wantCode: 405, wantAllow: "GET, HEAD"},
		{name: "remainder", method: "GET", target: "/files/a/b/c.txt", wantCode: 200, wantBody: `"a/b/c.txt"`},
		{name: "query", method: "GET", target: "/search?q=x&tag=a&tag=b", wantCode: 200, wantBody: "tags=[a b]"},
		{name: "query invalid", method: "GET", target: "/search?limit=0", wantCode: 400},
		{name: "strip prefix", method: "GET", target: "/api/v1/users/7", wantCode: 200, wantBody: "path seen by sub-mux: /users/7"},
		{name: "host mismatch", method: "GET", target: "/ping", wantCode: 404},
		{name: "host match", method: "GET", target: "/ping", host: "api.example.com", wantCode: 200, wantBody: "pong"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, nil)
			if tt.host != "" {
				req.Host = tt.host
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)

			if rec.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", rec.Code, tt.wantCode, rec.Body.String())
			}
			if !strings.Contains(rec.Body.String(), tt.wantBody) {
				t.Errorf("body %q does not contain %q", rec.Body.String(), tt.wantBody)
			}
			if got := rec.Header().Get("Allow"); got != tt.wantAllow {
				t.Errorf("Allow = %q, want %q", got, tt.wantAllow)
			}
		})
	}
}

func TestOverlappingPatternsPanic(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Error("expected a panic for conflicting patterns")
		}
	}()
	conflict2 := http.NewServeMux()
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	conflict2.Handle("/a/{x}/b", h)
	conflict2.Handle("/a/b/{y}", h)
}

func TestOverRealServer(t *testing.T) {
	ts := httptest.NewTestServer(t, newHandler())
	client := ts.Client() // call Client() first: it also sets ts.URL
	resp, err := client.Get(ts.URL + "/tasks/latest")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d", resp.StatusCode)
	}
}
