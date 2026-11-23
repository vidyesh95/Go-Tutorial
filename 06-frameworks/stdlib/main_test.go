package main

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The tests are the same in all four folders; only newTestHandler() and do() differ. That is the point:
// the HTTP contract is framework-independent.

func newTestHandler() http.Handler {
	return newHandler(NewStore(), slog.New(slog.DiscardHandler))
}

func do(t *testing.T, h http.Handler, method, path, body string) (int, string) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func errorCode(t *testing.T, body string) string {
	t.Helper()
	var e errorBody
	if err := json.Unmarshal([]byte(body), &e); err != nil {
		t.Fatalf("response is not the error envelope: %q (%v)", body, err)
	}
	return e.Error.Code
}

func TestNotesLifecycle(t *testing.T) {
	h := newTestHandler()

	if code, body := do(t, h, "GET", "/api/v1/notes", ""); code != 200 || strings.TrimSpace(body) != "[]" {
		t.Fatalf("empty list: got %d %q, want 200 []", code, body)
	}

	code, body := do(t, h, "POST", "/api/v1/notes", `{"title":"  hello  ","body":"world"}`)
	if code != 201 {
		t.Fatalf("create: got %d %q, want 201", code, body)
	}
	var n Note
	if err := json.Unmarshal([]byte(body), &n); err != nil {
		t.Fatal(err)
	}
	if n.ID != 1 || n.Title != "hello" || n.Body != "world" { // title is trimmed
		t.Fatalf("created note = %+v", n)
	}

	if code, body := do(t, h, "GET", "/api/v1/notes/1", ""); code != 200 || !strings.Contains(body, `"hello"`) {
		t.Fatalf("get: got %d %q", code, body)
	}
	if code, body := do(t, h, "GET", "/api/v1/notes", ""); code != 200 || !strings.Contains(body, `"hello"`) {
		t.Fatalf("list: got %d %q", code, body)
	}
	if code, _ := do(t, h, "DELETE", "/api/v1/notes/1", ""); code != 204 {
		t.Fatalf("delete: got %d, want 204", code)
	}
	code, body = do(t, h, "GET", "/api/v1/notes/1", "")
	if code != 404 || errorCode(t, body) != "not_found" {
		t.Fatalf("get after delete: got %d %q", code, body)
	}
}

func TestCreateValidation(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		wantCode int
		wantErr  string
	}{
		{"ok", `{"title":"fine"}`, 201, ""},
		{"exactly 100 chars", `{"title":"` + strings.Repeat("a", 100) + `"}`, 201, ""},
		{"101 chars", `{"title":"` + strings.Repeat("a", 101) + `"}`, 422, "validation_failed"},
		{"100 multibyte chars", `{"title":"` + strings.Repeat("é", 100) + `"}`, 201, ""},
		{"missing title", `{"body":"x"}`, 422, "validation_failed"},
		{"empty title", `{"title":""}`, 422, "validation_failed"},
		{"blank title", `{"title":"   "}`, 422, "validation_failed"},
		{"not json", `{oops`, 400, "bad_json"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			code, body := do(t, newTestHandler(), "POST", "/api/v1/notes", tc.body)
			if code != tc.wantCode {
				t.Fatalf("status = %d, want %d (body %q)", code, tc.wantCode, body)
			}
			if tc.wantErr != "" && errorCode(t, body) != tc.wantErr {
				t.Fatalf("error code = %q, want %q", errorCode(t, body), tc.wantErr)
			}
		})
	}
}

func TestBadAndMissingIDs(t *testing.T) {
	h := newTestHandler()
	for _, method := range []string{"GET", "DELETE"} {
		code, body := do(t, h, method, "/api/v1/notes/abc", "")
		if code != 400 || errorCode(t, body) != "invalid_id" {
			t.Errorf("%s /notes/abc: got %d %q", method, code, body)
		}
		code, body = do(t, h, method, "/api/v1/notes/42", "")
		if code != 404 || errorCode(t, body) != "not_found" {
			t.Errorf("%s /notes/42: got %d %q", method, code, body)
		}
	}
}

func TestPanicIsRecovered(t *testing.T) {
	h := newTestHandler()
	code, body := do(t, h, "GET", "/api/v1/boom", "")
	if code != 500 || errorCode(t, body) != "internal" {
		t.Fatalf("panic route: got %d %q, want 500 envelope", code, body)
	}
	// The server must still work after a panic.
	if code, _ := do(t, h, "GET", "/api/v1/notes", ""); code != 200 {
		t.Fatalf("after panic: got %d, want 200", code)
	}
}
