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
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth/githubtest"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/mailer"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/service"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/store"
)

// fastParams are tiny Argon2id parameters so the many signups in these tests stay quick.
var fastParams = auth.Params{Memory: 64, Iterations: 1, Parallelism: 1, SaltLen: 16, KeyLen: 32}

// testClock is the clock injected into the session service, so tests can age sessions and
// tokens without sleeping.
type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

var testSecret = []byte("0123456789abcdef0123456789abcdef")

// testEnv is a fully wired API on a real, migrated SQLite database in a temp dir, served by
// httptest. Tests exercise it only through HTTP, exactly as a client would.
type testEnv struct {
	srv      *httptest.Server
	client   *http.Client
	draining *atomic.Bool

	mail   *mailer.Memory     // captures emails instead of sending them
	clock  *testClock         // the session service's clock
	github *githubtest.Server // nil unless the env was built withGitHub
	store  *store.Store
}

type envOptions struct {
	rateRPS      float64
	rateBurst    int
	authRateRPS  float64
	authRate     int
	corsOrigins  []string
	cookieSecure bool
	withGitHub   bool
}

func newTestEnv(t *testing.T, opts ...func(*envOptions)) *testEnv {
	t.Helper()
	// Effectively unlimited unless a test says otherwise. cookieSecure is false because the test
	// server speaks plain http and Go's cookie jar (like a browser) never sends Secure cookies
	// over http.
	o := envOptions{rateRPS: 1000, rateBurst: 1000, authRateRPS: 1000, authRate: 1000}
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

	hasher, err := auth.NewPasswordHasher(fastParams)
	if err != nil {
		t.Fatal(err)
	}
	issuer, err := auth.NewIssuer(testSecret, time.Hour, nil)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.DiscardHandler)
	authSvc, err := service.NewAuthService(st, st, hasher, issuer, log)
	if err != nil {
		t.Fatal(err)
	}
	clock := &testClock{t: time.Now().UTC()}
	mail := &mailer.Memory{}
	sessionSvc, err := service.NewSessionAuthService(service.SessionAuthDeps{
		Auth: authSvc, Users: st, Accounts: st, Sessions: st, Verifications: st,
		Hasher: hasher, Mailer: mail, Log: log,
		Config: service.SessionConfig{PublicURL: "https://api.example.test", Now: clock.Now},
	})
	if err != nil {
		t.Fatal(err)
	}

	var fake *githubtest.Server
	var gh *auth.GitHub
	if o.withGitHub {
		fake = githubtest.New(t)
		// The redirect URL only has to be well-formed: the tests play the browser themselves.
		if gh, err = auth.NewGitHub(fake.Config("http://localhost/v1/session-auth/oauth/github/callback")); err != nil {
			t.Fatal(err)
		}
	}

	draining := &atomic.Bool{}
	handler := NewHandler(t.Context(), Deps{
		Logger:   log,
		Auth:     authSvc,
		Projects: service.NewProjectService(st),
		Tasks:    service.NewTaskService(st, st),
		Tokens:   issuer,
		DB:       db,
		Draining: draining,
		RateRPS:  o.rateRPS, RateBurst: o.rateBurst,
		CORSOrigins: o.corsOrigins,

		SessionAuth: sessionSvc, GitHub: gh, CookieSecure: o.cookieSecure,
		AuthRateRPS: o.authRateRPS, AuthRateBurst: o.authRate,
	})
	srv := httptest.NewTestServer(t, handler)
	return &testEnv{srv: srv, client: srv.Client(), draining: draining, mail: mail, clock: clock, github: fake, store: st}
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
