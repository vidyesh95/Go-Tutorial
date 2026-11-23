package httpapi

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/service"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/store"
)

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// testEnv is a fully wired API on a real, migrated SQLite database in a temp dir, served by
// httptest. Tests exercise it only through HTTP, exactly as a client would.
type testEnv struct {
	srv      *httptest.Server
	client   *http.Client
	draining *atomic.Bool
}

type envOptions struct {
	rateRPS     float64
	rateBurst   int
	corsOrigins []string
}

func newTestEnv(t *testing.T, opts ...func(*envOptions)) *testEnv {
	t.Helper()
	o := envOptions{rateRPS: 1000, rateBurst: 1000} // effectively unlimited unless a test says otherwise
	for _, f := range opts {
		f(&o)
	}

	db, err := store.Open(t.Context(), filepath.Join(t.TempDir(), "api.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := store.Migrate(t.Context(), db); err != nil {
		t.Fatal(err)
	}
	st := store.New(db)

	hasher, _ := auth.NewPasswordHasher(bcrypt.MinCost) // fast; the cost is irrelevant to HTTP behaviour
	issuer, err := auth.NewIssuer(testSecret, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	authSvc, err := service.NewAuthService(st, hasher, issuer)
	if err != nil {
		t.Fatal(err)
	}

	draining := &atomic.Bool{}
	handler := NewHandler(t.Context(), Deps{
		Logger:   slog.New(slog.DiscardHandler),
		Auth:     authSvc,
		Projects: service.NewProjectService(st),
		Tasks:    service.NewTaskService(st, st),
		Tokens:   issuer,
		DB:       db,
		Draining: draining,
		RateRPS:  o.rateRPS, RateBurst: o.rateBurst,
		CORSOrigins: o.corsOrigins,
	})
	srv := httptest.NewTestServer(t, handler)
	return &testEnv{srv: srv, client: srv.Client(), draining: draining}
}

// resp is a decoded HTTP response.
type resp struct {
	status int
	header http.Header
	raw    []byte
}

// json decodes the body into a generic map.
func (r resp) obj(t *testing.T) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.raw, &m); err != nil {
		t.Fatalf("body is not a JSON object: %v\n%s", err, r.raw)
	}
	return m
}

// do sends a request. body may be a string (sent verbatim), nil, or any value (JSON-encoded).
func (e *testEnv) do(t *testing.T, method, path, token string, body any) resp {
	t.Helper()
	var rd io.Reader
	switch b := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(b)
	default:
		buf, err := json.Marshal(b)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, e.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := e.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, raw: raw}
}

// signupAndLogin creates an account and returns its id and bearer token.
func (e *testEnv) signupAndLogin(t *testing.T, email string) (id, token string) {
	t.Helper()
	r := e.do(t, "POST", "/v1/auth/signup", "", map[string]string{"email": email, "name": "Test", "password": "correct horse"})
	if r.status != http.StatusCreated {
		t.Fatalf("signup %s: %d %s", email, r.status, r.raw)
	}
	id = r.obj(t)["id"].(string)
	r = e.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": email, "password": "correct horse"})
	if r.status != http.StatusOK {
		t.Fatalf("login %s: %d %s", email, r.status, r.raw)
	}
	return id, r.obj(t)["token"].(string)
}

func (e *testEnv) createProject(t *testing.T, token, name string) string {
	t.Helper()
	r := e.do(t, "POST", "/v1/projects", token, map[string]string{"name": name})
	if r.status != http.StatusCreated {
		t.Fatalf("create project: %d %s", r.status, r.raw)
	}
	return r.obj(t)["id"].(string)
}

func (e *testEnv) createTask(t *testing.T, token, projectID, title string) string {
	t.Helper()
	r := e.do(t, "POST", "/v1/projects/"+projectID+"/tasks", token, map[string]any{"title": title})
	if r.status != http.StatusCreated {
		t.Fatalf("create task: %d %s", r.status, r.raw)
	}
	return r.obj(t)["id"].(string)
}

// errorCode extracts error.code from an error envelope.
func errorCode(t *testing.T, r resp) string {
	t.Helper()
	e, ok := r.obj(t)["error"].(map[string]any)
	if !ok {
		t.Fatalf("not an error envelope: %s", r.raw)
	}
	return e["code"].(string)
}
