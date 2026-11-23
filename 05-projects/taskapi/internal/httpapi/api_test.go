package httpapi

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
)

// TestFullFlow walks the happy path the way a real client would.
func TestFullFlow(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// --- signup / login / me -------------------------------------------------------------
	signup := env.do(t, "POST", "/v1/auth/signup", "", map[string]string{"email": "Ada@Example.com", "name": "Ada", "password": "correct horse"})
	if signup.status != http.StatusCreated {
		t.Fatalf("signup: %d %s", signup.status, signup.raw)
	}
	if strings.Contains(string(signup.raw), "password") || strings.Contains(string(signup.raw), "$argon2") {
		t.Fatalf("signup response leaks password material: %s", signup.raw)
	}
	if got := signup.obj(t)["email"]; got != "ada@example.com" {
		t.Fatalf("email not normalized: %v", got)
	}

	login := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ada@example.com", "password": "correct horse"})
	if login.status != http.StatusOK {
		t.Fatalf("login: %d %s", login.status, login.raw)
	}
	token := login.obj(t)["token"].(string)
	if _, err := time.Parse(time.RFC3339, login.obj(t)["expires_at"].(string)); err != nil {
		t.Fatalf("expires_at: %v", err)
	}

	if me := env.do(t, "GET", "/v1/me", token, nil); me.status != 200 || me.obj(t)["name"] != "Ada" {
		t.Fatalf("me: %d %s", me.status, me.raw)
	}

	// --- projects --------------------------------------------------------------------------
	create := env.do(t, "POST", "/v1/projects", token, map[string]string{"name": "Launch"})
	if create.status != http.StatusCreated {
		t.Fatalf("create project: %d %s", create.status, create.raw)
	}
	pid := create.obj(t)["id"].(string)
	if loc := create.header.Get("Location"); loc != "/v1/projects/"+pid {
		t.Fatalf("Location = %q", loc)
	}
	if r := env.do(t, "GET", "/v1/projects/"+pid, token, nil); r.status != 200 || r.obj(t)["name"] != "Launch" {
		t.Fatalf("get project: %d %s", r.status, r.raw)
	}
	if r := env.do(t, "PATCH", "/v1/projects/"+pid, token, map[string]string{"name": "Launch v2"}); r.status != 200 || r.obj(t)["name"] != "Launch v2" {
		t.Fatalf("rename: %d %s", r.status, r.raw)
	}
	list := env.do(t, "GET", "/v1/projects", token, nil)
	if items := list.obj(t)["items"].([]any); list.status != 200 || len(items) != 1 || list.obj(t)["next_cursor"] != nil {
		t.Fatalf("list projects: %d %s", list.status, list.raw)
	}

	// --- tasks -----------------------------------------------------------------------------
	var taskIDs []string
	for i := range 5 {
		taskIDs = append(taskIDs, env.createTask(t, token, pid, fmt.Sprintf("Task %d", i)))
	}
	create5 := env.do(t, "POST", "/v1/projects/"+pid+"/tasks", token,
		map[string]any{"title": "Ship", "priority": 1, "due_date": "2026-12-31"})
	if create5.status != 201 || create5.obj(t)["priority"] != float64(1) || create5.obj(t)["due_date"] != "2026-12-31" ||
		create5.header.Get("Location") != "/v1/tasks/"+create5.obj(t)["id"].(string) {
		t.Fatalf("create task: %d %s", create5.status, create5.raw)
	}

	// Mark two tasks done via PATCH; status defaults to todo.
	for _, id := range taskIDs[:2] {
		r := env.do(t, "PATCH", "/v1/tasks/"+id, token, map[string]string{"status": "done"})
		if r.status != 200 || r.obj(t)["status"] != "done" {
			t.Fatalf("patch: %d %s", r.status, r.raw)
		}
	}
	// PATCH leaves other fields alone, and "" clears the due date.
	r := env.do(t, "PATCH", "/v1/tasks/"+create5.obj(t)["id"].(string), token, map[string]string{"due_date": ""})
	if r.status != 200 || r.obj(t)["due_date"] != nil || r.obj(t)["title"] != "Ship" || r.obj(t)["priority"] != float64(1) {
		t.Fatalf("clear due date: %d %s", r.status, r.raw)
	}

	// Filter by status.
	done := env.do(t, "GET", "/v1/projects/"+pid+"/tasks?status=done", token, nil)
	if items := done.obj(t)["items"].([]any); len(items) != 2 {
		t.Fatalf("status=done: %s", done.raw)
	}

	// Cursor pagination over all 6 tasks, 4 per page: pages of 4 and 2.
	p1 := env.do(t, "GET", "/v1/projects/"+pid+"/tasks?limit=4", token, nil).obj(t)
	cursor, _ := p1["next_cursor"].(string)
	if len(p1["items"].([]any)) != 4 || cursor == "" {
		t.Fatalf("page 1: %v", p1)
	}
	p2 := env.do(t, "GET", "/v1/projects/"+pid+"/tasks?limit=4&cursor="+cursor, token, nil).obj(t)
	if len(p2["items"].([]any)) != 2 || p2["next_cursor"] != nil {
		t.Fatalf("page 2: %v", p2)
	}
	seen := map[string]bool{}
	for _, page := range []map[string]any{p1, p2} {
		for _, it := range page["items"].([]any) {
			seen[it.(map[string]any)["id"].(string)] = true
		}
	}
	if len(seen) != 6 {
		t.Fatalf("pages overlapped or skipped rows: %d distinct tasks", len(seen))
	}

	// --- delete ----------------------------------------------------------------------------
	if r := env.do(t, "DELETE", "/v1/tasks/"+taskIDs[0], token, nil); r.status != http.StatusNoContent || len(r.raw) != 0 {
		t.Fatalf("delete task: %d %s", r.status, r.raw)
	}
	if r := env.do(t, "GET", "/v1/tasks/"+taskIDs[0], token, nil); r.status != http.StatusNotFound {
		t.Fatalf("get deleted task: %d", r.status)
	}
	if r := env.do(t, "DELETE", "/v1/projects/"+pid, token, nil); r.status != http.StatusNoContent {
		t.Fatalf("delete project: %d %s", r.status, r.raw)
	}
	// The project's remaining tasks went with it.
	if r := env.do(t, "GET", "/v1/tasks/"+taskIDs[1], token, nil); r.status != http.StatusNotFound {
		t.Fatalf("task survived project delete: %d", r.status)
	}
}

// TestOtherUsersResourcesAre404 is the core authorization test: user B must not be able to
// read, change or even confirm the existence of user A's data.
func TestOtherUsersResourcesAre404(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, alice := env.signupAndLogin(t, "alice@example.com")
	_, bob := env.signupAndLogin(t, "bob@example.com")

	pid := env.createProject(t, alice, "Alice's project")
	tid := env.createTask(t, alice, pid, "Alice's task")

	attempts := []struct {
		method, path string
		body         any
	}{
		{"GET", "/v1/projects/" + pid, nil},
		{"PATCH", "/v1/projects/" + pid, map[string]string{"name": "hijacked"}},
		{"DELETE", "/v1/projects/" + pid, nil},
		{"GET", "/v1/projects/" + pid + "/tasks", nil},
		{"POST", "/v1/projects/" + pid + "/tasks", map[string]string{"title": "planted"}},
		{"GET", "/v1/tasks/" + tid, nil},
		{"PATCH", "/v1/tasks/" + tid, map[string]string{"status": "done"}},
		{"DELETE", "/v1/tasks/" + tid, nil},
	}
	for _, a := range attempts {
		t.Run(a.method+" "+a.path, func(t *testing.T) {
			r := env.do(t, a.method, a.path, bob, a.body)
			if r.status != http.StatusNotFound || errorCode(t, r) != "not_found" {
				t.Fatalf("got %d %s, want 404 not_found (never 403)", r.status, r.raw)
			}
		})
	}

	// Bob's own listing is empty; Alice's data is untouched.
	if r := env.do(t, "GET", "/v1/projects", bob, nil); len(r.obj(t)["items"].([]any)) != 0 {
		t.Fatalf("bob sees alice's projects: %s", r.raw)
	}
	if r := env.do(t, "GET", "/v1/tasks/"+tid, alice, nil); r.status != 200 || r.obj(t)["status"] != "todo" || r.obj(t)["title"] != "Alice's task" {
		t.Fatalf("alice's task was modified: %s", r.raw)
	}
}

func TestAuthentication(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	id, good := env.signupAndLogin(t, "ada@example.com")

	// A well-formed token that has expired (issued by the same secret, but two days ago).
	past := func() time.Time { return time.Now().Add(-48 * time.Hour) }
	oldIssuer, _ := auth.NewIssuer(testSecret, time.Hour, past)
	expired, _, _ := oldIssuer.Issue(id)
	// A token signed with a different key.
	forgeIssuer, _ := auth.NewIssuer([]byte("another-secret-another-secret-xx"), time.Hour, nil)
	forged, _, _ := forgeIssuer.Issue(id)

	for _, tc := range []struct {
		name   string
		header string
	}{
		{"no header", ""},
		{"wrong scheme", "Basic dXNlcjpwYXNz"},
		{"empty bearer", "Bearer "},
		{"garbage", "Bearer not-a-jwt"},
		{"expired", "Bearer " + expired},
		{"wrong signing key", "Bearer " + forged},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/v1/me", nil)
			if tc.header != "" {
				req.Header.Set("Authorization", tc.header)
			}
			res, err := env.client.Do(req)
			if err != nil {
				t.Fatal(err)
			}
			res.Body.Close()
			if res.StatusCode != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", res.StatusCode)
			}
			if res.Header.Get("WWW-Authenticate") == "" {
				t.Error("missing WWW-Authenticate header")
			}
		})
	}

	// The scheme is case-insensitive per RFC 7235.
	req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/v1/me", nil)
	req.Header.Set("Authorization", "bearer "+good)
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatalf("lowercase scheme: status %d", res.StatusCode)
	}

	// Every protected route requires auth.
	for _, route := range []string{
		"GET /v1/projects", "POST /v1/projects", "GET /v1/projects/x", "PATCH /v1/projects/x", "DELETE /v1/projects/x",
		"GET /v1/projects/x/tasks", "POST /v1/projects/x/tasks", "GET /v1/tasks/x", "PATCH /v1/tasks/x", "DELETE /v1/tasks/x",
	} {
		method, path, _ := strings.Cut(route, " ")
		if r := env.do(t, method, path, "", nil); r.status != http.StatusUnauthorized {
			t.Errorf("%s without token = %d, want 401", route, r.status)
		}
	}
}

func TestLoginFailuresAreIndistinguishable(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.signupAndLogin(t, "ada@example.com")

	wrongPw := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ada@example.com", "password": "wrong-password"})
	noUser := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ghost@example.com", "password": "wrong-password"})
	if wrongPw.status != 401 || noUser.status != 401 {
		t.Fatalf("statuses = %d, %d", wrongPw.status, noUser.status)
	}
	// Bodies are identical apart from the per-request id.
	strip := func(r resp) string {
		m := r.obj(t)["error"].(map[string]any)
		delete(m, "request_id")
		return fmt.Sprint(m)
	}
	if strip(wrongPw) != strip(noUser) {
		t.Fatalf("responses differ:\n%s\n%s", wrongPw.raw, noUser.raw)
	}
}

func TestValidationAndBadInput(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, token := env.signupAndLogin(t, "ada@example.com")
	pid := env.createProject(t, token, "P")

	tests := []struct {
		name       string
		method     string
		path       string
		body       any
		wantStatus int
		wantCode   string
		wantField  string // expected key in error.fields (422 only)
	}{
		{"signup weak password", "POST", "/v1/auth/signup", map[string]string{"email": "x@example.com", "name": "X", "password": "short"}, 422, "validation_failed", "password"},
		{"signup bad email + empty name", "POST", "/v1/auth/signup", map[string]string{"email": "nope", "name": "", "password": "long enough pw"}, 422, "validation_failed", "email"},
		{"project blank name", "POST", "/v1/projects", map[string]string{"name": "  "}, 422, "validation_failed", "name"},
		{"task missing title", "POST", "/v1/projects/" + pid + "/tasks", map[string]any{"priority": 2}, 422, "validation_failed", "title"},
		{"task priority out of range", "POST", "/v1/projects/" + pid + "/tasks", map[string]any{"title": "x", "priority": 9}, 422, "validation_failed", "priority"},
		{"task impossible date", "POST", "/v1/projects/" + pid + "/tasks", map[string]any{"title": "x", "due_date": "2026-02-31"}, 422, "validation_failed", "due_date"},
		{"patch empty", "PATCH", "/v1/tasks/whatever", map[string]any{}, 422, "validation_failed", "body"},
		{"patch bad status", "PATCH", "/v1/tasks/whatever", map[string]any{"status": "finished"}, 422, "validation_failed", "status"},
		{"list bad status filter", "GET", "/v1/projects/" + pid + "/tasks?status=nope", nil, 422, "validation_failed", "status"},
		{"list bad cursor", "GET", "/v1/projects/" + pid + "/tasks?cursor=!!!", nil, 422, "validation_failed", "cursor"},
		{"list bad limit", "GET", "/v1/projects?limit=abc", nil, 422, "validation_failed", "limit"},
		{"list zero limit", "GET", "/v1/projects?limit=0", nil, 422, "validation_failed", "limit"},

		{"malformed JSON", "POST", "/v1/projects", `{"name": `, 400, "bad_request", ""},
		{"not JSON at all", "POST", "/v1/projects", `hello`, 400, "bad_request", ""},
		{"empty body", "POST", "/v1/projects", ``, 400, "bad_request", ""},
		{"unknown field", "POST", "/v1/projects", `{"name":"x","admin":true}`, 400, "bad_request", ""},
		{"wrong type", "POST", "/v1/projects", `{"name": 42}`, 400, "bad_request", ""},
		{"trailing data", "POST", "/v1/projects", `{"name":"x"}{"name":"y"}`, 400, "bad_request", ""},
		{"body too large", "POST", "/v1/projects", `{"name":"` + strings.Repeat("a", 2<<20) + `"}`, 413, "payload_too_large", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := tt.body
			if s, ok := body.(string); ok && s == "" {
				body = nil
			}
			r := env.do(t, tt.method, tt.path, token, body)
			if r.status != tt.wantStatus || errorCode(t, r) != tt.wantCode {
				t.Fatalf("got %d %s, want %d %s", r.status, r.raw, tt.wantStatus, tt.wantCode)
			}
			if tt.wantField != "" {
				fields, _ := r.obj(t)["error"].(map[string]any)["fields"].(map[string]any)
				if _, ok := fields[tt.wantField]; !ok {
					t.Fatalf("fields = %v, want key %q", fields, tt.wantField)
				}
			}
			if r.header.Get("Content-Type") != "application/json; charset=utf-8" {
				t.Fatalf("Content-Type = %q", r.header.Get("Content-Type"))
			}
		})
	}
}

func TestConflicts(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.signupAndLogin(t, "ada@example.com")
	_, token := env.signupAndLogin(t, "other@example.com")

	dup := env.do(t, "POST", "/v1/auth/signup", "", map[string]string{"email": "ADA@example.com", "name": "Again", "password": "correct horse"})
	if dup.status != http.StatusConflict || errorCode(t, dup) != "conflict" {
		t.Fatalf("duplicate signup: %d %s", dup.status, dup.raw)
	}

	env.createProject(t, token, "Same")
	if r := env.do(t, "POST", "/v1/projects", token, map[string]string{"name": "Same"}); r.status != http.StatusConflict {
		t.Fatalf("duplicate project: %d %s", r.status, r.raw)
	}
	// Renaming onto an existing name conflicts too.
	other := env.createProject(t, token, "Other")
	if r := env.do(t, "PATCH", "/v1/projects/"+other, token, map[string]string{"name": "Same"}); r.status != http.StatusConflict {
		t.Fatalf("rename conflict: %d %s", r.status, r.raw)
	}
}

func TestRateLimit(t *testing.T) {
	t.Parallel()
	// 1 request/second with a burst of 3: the first three pass, the rest must be rejected.
	env := newTestEnv(t, func(o *envOptions) { o.rateRPS, o.rateBurst = 1, 3 })

	var codes []int
	var limited resp
	for range 6 {
		r := env.do(t, "GET", "/healthz", "", nil)
		codes = append(codes, r.status)
		if r.status == http.StatusTooManyRequests {
			limited = r
		}
	}
	if fmt.Sprint(codes[:3]) != "[200 200 200]" || codes[3] != 429 {
		t.Fatalf("status sequence = %v", codes)
	}
	if limited.header.Get("Retry-After") == "" || errorCode(t, limited) != "rate_limited" {
		t.Fatalf("429 response: %v %s", limited.header, limited.raw)
	}
}

func TestOperationalEndpoints(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	if r := env.do(t, "GET", "/healthz", "", nil); r.status != 200 || r.obj(t)["status"] != "ok" {
		t.Fatalf("healthz: %d %s", r.status, r.raw)
	}
	if r := env.do(t, "GET", "/readyz", "", nil); r.status != 200 {
		t.Fatalf("readyz: %d %s", r.status, r.raw)
	}
	env.draining.Store(true)
	if r := env.do(t, "GET", "/readyz", "", nil); r.status != 503 || r.obj(t)["status"] != "shutting_down" {
		t.Fatalf("readyz while draining: %d %s", r.status, r.raw)
	}
	// Liveness stays green while draining: the process is alive, just not accepting new work.
	if r := env.do(t, "GET", "/healthz", "", nil); r.status != 200 {
		t.Fatalf("healthz while draining: %d", r.status)
	}
}

func TestMetricsUseRoutePatterns(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, token := env.signupAndLogin(t, "ada@example.com")
	pid := env.createProject(t, token, "P")
	env.do(t, "GET", "/v1/projects/"+pid, token, nil)
	env.do(t, "GET", "/v1/projects/"+pid, "", nil) // 401
	env.do(t, "GET", "/v1/no/such/route", "", nil)
	env.do(t, "BREW", "/v1/projects", token, nil)

	body := string(env.do(t, "GET", "/metrics", "", nil).raw)
	for _, want := range []string{
		`http_requests_total{method="GET",route="/v1/projects/{id}",status="200"} 1`,
		`http_requests_total{method="GET",route="/v1/projects/{id}",status="401"} 1`,
		`http_requests_total{method="POST",route="/v1/projects",status="201"} 1`,
		`http_requests_total{method="GET",route="unmatched",status="404"} 1`,
		`http_requests_total{method="OTHER",route="unmatched",status="405"} 1`,
		`http_request_duration_seconds_bucket{method="GET",route="/v1/projects/{id}",le="`,
		"go_goroutines",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics missing %q", want)
		}
	}
	// The raw id must never appear as a label value: that would be unbounded cardinality.
	if strings.Contains(body, pid) {
		t.Errorf("/metrics contains a raw resource id %s", pid)
	}
}

func TestRequestID(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	generated := env.do(t, "GET", "/v1/me", "", nil)
	rid := generated.header.Get("X-Request-ID")
	if rid == "" {
		t.Fatal("no X-Request-ID generated")
	}
	if got := generated.obj(t)["error"].(map[string]any)["request_id"]; got != rid {
		t.Fatalf("error body request_id = %v, header = %s", got, rid)
	}

	req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/healthz", nil)
	req.Header.Set("X-Request-ID", "client-id-123")
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Request-ID"); got != "client-id-123" {
		t.Fatalf("sane client id not preserved: %q", got)
	}

	// (Go's client refuses to send a newline in a header, but other clients and proxies can.)
	req.Header.Set("X-Request-ID", "evil id with spaces")
	res, err = env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if got := res.Header.Get("X-Request-ID"); got == "" || strings.Contains(got, " ") {
		t.Fatalf("unsafe client id was not replaced: %q", got)
	}
}

func TestCORS(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.corsOrigins = []string{"https://app.example"} })

	preflight := func(origin string) *http.Response {
		req, _ := http.NewRequestWithContext(t.Context(), "OPTIONS", env.srv.URL+"/v1/projects", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", "POST")
		res, err := env.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res
	}

	ok := preflight("https://app.example")
	if ok.StatusCode != 204 || ok.Header.Get("Access-Control-Allow-Origin") != "https://app.example" ||
		!strings.Contains(ok.Header.Get("Access-Control-Allow-Headers"), "Authorization") {
		t.Fatalf("allowed preflight: %d %v", ok.StatusCode, ok.Header)
	}
	if bad := preflight("https://evil.example"); bad.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatalf("disallowed origin got CORS headers: %v", bad.Header)
	}
	if bad := preflight("https://evil.example"); bad.StatusCode == 204 {
		t.Fatal("disallowed preflight must not succeed")
	}
}

// Panics in a handler must become a clean 500 envelope, not a dropped connection.
func TestRecovererReturns500(t *testing.T) {
	t.Parallel()
	var logs strings.Builder
	log := newTestLogger(&logs)
	h := chain(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") }),
		requestID, recoverer(log))

	rec := newRecorder()
	h.ServeHTTP(rec, newRequest("GET", "/"))
	if rec.Code != 500 || !strings.Contains(rec.Body.String(), `"code":"internal"`) {
		t.Fatalf("got %d %s", rec.Code, rec.Body)
	}
	if strings.Contains(rec.Body.String(), "boom") {
		t.Fatal("panic value leaked to the client")
	}
	if !strings.Contains(logs.String(), "boom") || !strings.Contains(logs.String(), "stack") {
		t.Fatalf("panic not logged with stack: %s", logs.String())
	}
}
