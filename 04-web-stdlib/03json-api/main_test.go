package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// do runs one request through the handler without a network.
func do(t *testing.T, h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	var req *http.Request
	if body == "" {
		req = httptest.NewRequest(method, target, nil)
	} else {
		req = httptest.NewRequest(method, target, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func newTestAPI() http.Handler { return (&api{store: newTaskStore()}).routes() }

func decode[T any](t *testing.T, rec *httptest.ResponseRecorder) T {
	t.Helper()
	var v T
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not valid JSON: %v\n%s", err, rec.Body.String())
	}
	return v
}

func TestCreateReturns201AndLocation(t *testing.T) {
	h := newTestAPI()
	rec := do(t, h, "POST", "/tasks", `{"title":"write docs"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201: %s", rec.Code, rec.Body)
	}
	task := decode[Task](t, rec)
	if want := "/tasks/" + task.ID.String(); rec.Header().Get("Location") != want {
		t.Errorf("Location = %q, want %q", rec.Header().Get("Location"), want)
	}
	if task.Priority != defaultPriority {
		t.Errorf("priority = %d, want default %d", task.Priority, defaultPriority)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q", ct)
	}
}

func TestErrors(t *testing.T) {
	h := newTestAPI()
	do(t, h, "POST", "/tasks", `{"title":"taken"}`)

	tests := []struct {
		name, method, target, body string
		wantStatus                 int
		wantCode                   string
	}{
		{"validation", "POST", "/tasks", `{"title":"","priority":0}`, 422, "validation_failed"},
		{"unknown field", "POST", "/tasks", `{"title":"x","bogus":1}`, 400, "bad_request"},
		{"malformed", "POST", "/tasks", `{"title":`, 400, "bad_request"},
		{"trailing garbage", "POST", "/tasks", `{"title":"x"} {"title":"y"}`, 400, "bad_request"},
		{"empty body", "POST", "/tasks", ``, 400, "bad_request"},
		{"too large", "POST", "/tasks", `{"title":"` + strings.Repeat("a", maxBodyBytes) + `"}`, 413, "body_too_large"},
		{"duplicate", "POST", "/tasks", `{"title":"TAKEN"}`, 409, "conflict"},
		{"unknown id", "GET", "/tasks/01a207fb-efff-7537-bb76-ef022a6a154b", "", 404, "not_found"},
		{"malformed id", "GET", "/tasks/not-a-uuid", "", 404, "not_found"},
		{"bad filter", "GET", "/tasks?done=maybe", "", 400, "bad_request"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := do(t, h, tt.method, tt.target, tt.body)
			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tt.wantStatus, rec.Body)
			}
			env := decode[errorEnvelope](t, rec)
			if env.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", env.Error.Code, tt.wantCode)
			}
		})
	}
}

func TestValidationReturnsFieldErrors(t *testing.T) {
	rec := do(t, newTestAPI(), "POST", "/tasks", `{"title":" ","priority":9}`)
	env := decode[errorEnvelope](t, rec)
	if env.Error.Fields["title"] == "" || env.Error.Fields["priority"] == "" {
		t.Errorf("fields = %v, want title and priority", env.Error.Fields)
	}
}

func TestCRUDFlow(t *testing.T) {
	h := newTestAPI()
	created := decode[Task](t, do(t, h, "POST", "/tasks", `{"title":"a","priority":1}`))
	path := "/tasks/" + created.ID.String()

	patched := decode[Task](t, do(t, h, "PATCH", path, `{"done":true}`))
	if !patched.Done || patched.Title != "a" || patched.Priority != 1 {
		t.Errorf("PATCH changed more than it should: %+v", patched)
	}

	// PATCH with done:false must be distinguishable from "field absent".
	patched = decode[Task](t, do(t, h, "PATCH", path, `{"done":false}`))
	if patched.Done {
		t.Error("done:false was ignored")
	}

	if rec := do(t, h, "DELETE", path, ""); rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Errorf("DELETE = %d body=%q, want 204 and empty body", rec.Code, rec.Body)
	}
	if rec := do(t, h, "GET", path, ""); rec.Code != http.StatusNotFound {
		t.Errorf("GET after delete = %d, want 404", rec.Code)
	}
	if rec := do(t, h, "DELETE", path, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second DELETE = %d, want 404", rec.Code)
	}
}

func TestListIsInCreationOrder(t *testing.T) {
	h := newTestAPI()
	titles := []string{"one", "two", "three", "four", "five"}
	for _, title := range titles {
		do(t, h, "POST", "/tasks", `{"title":"`+title+`"}`)
	}
	got := decode[struct{ Tasks []Task }](t, do(t, h, "GET", "/tasks", ""))
	if len(got.Tasks) != len(titles) {
		t.Fatalf("len = %d", len(got.Tasks))
	}
	for i, task := range got.Tasks {
		if task.Title != titles[i] {
			t.Errorf("position %d = %q, want %q (UUIDv7 ids should sort by creation)", i, task.Title, titles[i])
		}
	}
}

// Run with -race: proves the RWMutex really protects the map.
func TestConcurrentCreates(t *testing.T) {
	store := newTaskStore()
	var wg sync.WaitGroup
	for i := range 50 {
		wg.Go(func() {
			if _, err := store.Create(fmt.Sprintf("task-%d", i), 3); err != nil {
				t.Error(err)
			}
			store.List(nil)
		})
	}
	wg.Wait()
	if n := len(store.List(nil)); n != 50 {
		t.Errorf("len = %d, want 50", n)
	}
}

// One test over a real in-memory connection, closed automatically.
func TestOverNetwork(t *testing.T) {
	ts := httptest.NewTestServer(t, newTestAPI())
	client := ts.Client() // call Client() first: it also sets ts.URL
	resp, err := client.Post(ts.URL+"/tasks", "application/json", strings.NewReader(`{"title":"net"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d", resp.StatusCode)
	}
}
