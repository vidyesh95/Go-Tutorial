package httpapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth/githubtest"
)

// browser is an HTTP client with a cookie jar, standing in for a real browser: it stores the
// Set-Cookie it receives and sends it back, and it does not follow redirects (so tests can
// inspect them).
type browser struct {
	env    *testEnv
	client *http.Client
}

func (e *testEnv) newBrowser(t *testing.T) *browser {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return &browser{env: e, client: &http.Client{
		Jar: jar, Transport: e.client.Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// do sends a request; hdr is a flat list of header name, value pairs.
func (b *browser) do(t *testing.T, method, path string, body any, hdr ...string) resp {
	t.Helper()
	var rd io.Reader
	switch v := body.(type) {
	case nil:
	case string:
		rd = strings.NewReader(v)
	default:
		buf, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, b.env.srv.URL+path, rd)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		req.Header.Set(hdr[i], hdr[i+1])
	}
	res, err := b.client.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(res.Body)
	return resp{status: res.StatusCode, header: res.Header, raw: raw}
}

// sessionCookie returns the session cookie currently in the jar (nil if none).
func (b *browser) sessionCookie() *http.Cookie {
	u, _ := url.Parse(b.env.srv.URL)
	for _, c := range b.client.Jar.Cookies(u) {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

// setCookie finds a cookie in the Set-Cookie headers of the response.
func (r resp) setCookie(name string) *http.Cookie {
	for _, c := range (&http.Response{Header: r.header}).Cookies() {
		if c.Name == name {
			return c
		}
	}
	return nil
}

func (b *browser) signUp(t *testing.T, email string) resp {
	t.Helper()
	r := b.do(t, "POST", "/v1/session-auth/sign-up/email", map[string]string{"email": email, "name": "Ada", "password": "correct horse"})
	if r.status != http.StatusCreated {
		t.Fatalf("sign-up %s: %d %s", email, r.status, r.raw)
	}
	return r
}

func (b *browser) signIn(t *testing.T, email, password string) resp {
	t.Helper()
	return b.do(t, "POST", "/v1/session-auth/sign-in/email", map[string]string{"email": email, "password": password})
}

var mailTokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

// mailToken reads the token out of the newest captured email, like a user clicking the link.
func (e *testEnv) mailToken(t *testing.T) string {
	t.Helper()
	m, ok := e.mail.Last()
	if !ok {
		t.Fatal("no email captured")
	}
	g := mailTokenRe.FindStringSubmatch(m.Body)
	if g == nil {
		t.Fatalf("no token in email body:\n%s", m.Body)
	}
	return g[1]
}

// --- The main cookie flow ------------------------------------------------------------------

func TestSessionAuthFlow(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ada := env.newBrowser(t)

	// Sign up: 201, user + session in the body, the token ONLY in the cookie.
	su := ada.signUp(t, "Ada@Example.com")
	body := su.obj(t)
	user, session := body["user"].(map[string]any), body["session"].(map[string]any)
	if user["email"] != "ada@example.com" || user["email_verified"] != false || user["name"] != "Ada" {
		t.Fatalf("user = %v", user)
	}
	if session["id"] == "" || session["expires_at"] == nil || session["ip_address"] == "" || session["user_agent"] == "" {
		t.Fatalf("session = %v", session)
	}
	for _, leaked := range []string{"token", "token_hash", "password", "$argon2"} {
		if strings.Contains(string(su.raw), leaked) {
			t.Fatalf("response leaks %q: %s", leaked, su.raw)
		}
	}
	c := su.setCookie(sessionCookieName)
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Path != "/" || c.MaxAge != 7*24*3600 || c.Domain != "" {
		t.Fatalf("session cookie = %+v", c)
	}

	// The database holds only the HASH of the cookie value.
	if _, err := env.store.GetSessionByTokenHash(t.Context(), c.Value); err == nil {
		t.Fatal("the raw cookie value is stored in the database")
	}
	if s, err := env.store.GetSessionByTokenHash(t.Context(), auth.HashToken(c.Value)); err != nil || s.ID != session["id"] {
		t.Fatalf("hashed lookup: %+v, %v", s, err)
	}

	// get-session, /v1/me and the REST API all work with the cookie alone.
	gs := ada.do(t, "GET", "/v1/session-auth/get-session", nil)
	if gs.status != 200 || gs.obj(t)["user"].(map[string]any)["id"] != user["id"] || gs.obj(t)["session"].(map[string]any)["id"] != session["id"] {
		t.Fatalf("get-session: %d %s", gs.status, gs.raw)
	}
	if me := ada.do(t, "GET", "/v1/me", nil); me.status != 200 || me.obj(t)["id"] != user["id"] {
		t.Fatalf("/v1/me with cookie: %d %s", me.status, me.raw)
	}
	cp := ada.do(t, "POST", "/v1/projects", map[string]string{"name": "Via cookie"})
	if cp.status != http.StatusCreated {
		t.Fatalf("create project with cookie: %d %s", cp.status, cp.raw)
	}
	if lp := ada.do(t, "GET", "/v1/projects", nil); lp.status != 200 || len(lp.obj(t)["items"].([]any)) != 1 {
		t.Fatalf("list projects: %d %s", lp.status, lp.raw)
	}

	// A second device signs in: two sessions, exactly one marked current.
	phone := env.newBrowser(t)
	if r := phone.signIn(t, "ada@example.com", "correct horse"); r.status != 200 || r.setCookie(sessionCookieName) == nil {
		t.Fatalf("second sign-in: %d %s", r.status, r.raw)
	}
	ls := ada.do(t, "GET", "/v1/session-auth/list-sessions", nil)
	sessions := ls.obj(t)["sessions"].([]any)
	if ls.status != 200 || len(sessions) != 2 {
		t.Fatalf("list-sessions: %d %s", ls.status, ls.raw)
	}
	current := 0
	for _, s := range sessions {
		m := s.(map[string]any)
		if m["current"] == true {
			current++
			if m["id"] != session["id"] {
				t.Errorf("wrong session marked current: %v", m)
			}
		}
		if _, ok := m["token"]; ok {
			t.Error("list-sessions exposes a token")
		}
	}
	if current != 1 {
		t.Fatalf("%d sessions marked current, want 1", current)
	}

	// Revoke the other device's session: it is logged out, ours is not.
	if r := ada.do(t, "POST", "/v1/session-auth/revoke-other-sessions", nil); r.status != 200 {
		t.Fatalf("revoke-other-sessions: %d %s", r.status, r.raw)
	}
	if r := phone.do(t, "GET", "/v1/session-auth/get-session", nil); r.status != 401 {
		t.Fatalf("revoked device still signed in: %d", r.status)
	}
	if r := ada.do(t, "GET", "/v1/session-auth/get-session", nil); r.status != 200 {
		t.Fatalf("current session was revoked: %d", r.status)
	}

	// Sign out: cookie cleared, everything is 401 afterwards, even if the old cookie is replayed.
	oldValue := ada.sessionCookie().Value
	so := ada.do(t, "POST", "/v1/session-auth/sign-out", nil)
	if so.status != 200 {
		t.Fatalf("sign-out: %d %s", so.status, so.raw)
	}
	if cc := so.setCookie(sessionCookieName); cc == nil || cc.MaxAge >= 0 || cc.Value != "" {
		t.Fatalf("sign-out did not clear the cookie: %+v", cc)
	}
	for _, path := range []string{"/v1/session-auth/get-session", "/v1/projects", "/v1/me"} {
		if r := ada.do(t, "GET", path, nil); r.status != 401 {
			t.Errorf("GET %s after sign-out = %d, want 401", path, r.status)
		}
	}
	req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: oldValue})
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Fatalf("replayed old cookie = %d, want 401 (server-side revocation)", res.StatusCode)
	}
}

func TestSessionAuthRejectsBadCredentialsAndInput(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	b := env.newBrowser(t)
	b.signUp(t, "ada@example.com")

	wrong := env.newBrowser(t).signIn(t, "ada@example.com", "wrong password")
	ghost := env.newBrowser(t).signIn(t, "ghost@example.com", "wrong password")
	if wrong.status != 401 || ghost.status != 401 || wrong.setCookie(sessionCookieName) != nil {
		t.Fatalf("statuses %d %d", wrong.status, ghost.status)
	}
	strip := func(r resp) string {
		m := r.obj(t)["error"].(map[string]any)
		delete(m, "request_id")
		return fmt.Sprint(m)
	}
	if strip(wrong) != strip(ghost) {
		t.Fatalf("bad-password and unknown-email responses differ:\n%s\n%s", wrong.raw, ghost.raw)
	}

	if r := b.do(t, "POST", "/v1/session-auth/sign-up/email", map[string]string{"email": "ada@example.com", "name": "x", "password": "correct horse"}); r.status != 409 {
		t.Errorf("duplicate sign-up = %d", r.status)
	}
	if r := b.do(t, "POST", "/v1/session-auth/sign-up/email", map[string]string{"email": "bad", "name": "", "password": "short"}); r.status != 422 {
		t.Errorf("invalid sign-up = %d", r.status)
	}
	if r := b.do(t, "POST", "/v1/session-auth/sign-in/email", `{"email":`); r.status != 400 {
		t.Errorf("malformed JSON = %d", r.status)
	}

	// Without a session, the session endpoints are 401; sign-out without a cookie is still fine.
	anon := env.newBrowser(t)
	for _, route := range []string{"GET /v1/session-auth/get-session", "GET /v1/session-auth/list-sessions",
		"POST /v1/session-auth/revoke-session", "POST /v1/session-auth/revoke-other-sessions", "POST /v1/session-auth/send-verification-email"} {
		method, path, _ := strings.Cut(route, " ")
		var body any
		if method == "POST" {
			body = map[string]string{"id": "x"}
		}
		if r := anon.do(t, method, path, body); r.status != 401 {
			t.Errorf("%s anonymously = %d, want 401", route, r.status)
		}
	}
	if r := anon.do(t, "POST", "/v1/session-auth/sign-out", nil); r.status != 200 {
		t.Errorf("sign-out without cookie = %d", r.status)
	}
	// Tampered cookie values and unknown methods.
	req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/v1/me", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: "forged-value"})
	res, _ := env.client.Do(req)
	res.Body.Close()
	if res.StatusCode != 401 || (&http.Response{Header: res.Header}).Cookies()[0].MaxAge >= 0 {
		t.Fatalf("forged cookie = %d (cookie should also be cleared)", res.StatusCode)
	}
}

func TestSessionCookieIsSecureByDefaultConfig(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.cookieSecure = true })
	r := env.do(t, "POST", "/v1/session-auth/sign-up/email", "", map[string]string{"email": "a@example.com", "name": "A", "password": "correct horse"})
	if r.status != 201 {
		t.Fatalf("%d %s", r.status, r.raw)
	}
	header := strings.Join(r.header.Values("Set-Cookie"), "\n")
	for _, want := range []string{sessionCookieName + "=", "HttpOnly", "Secure", "SameSite=Lax", "Path=/", "Max-Age=604800"} {
		if !strings.Contains(header, want) {
			t.Errorf("Set-Cookie lacks %q: %s", want, header)
		}
	}
}

func TestSessionSlidingExpiryOverHTTP(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	b := env.newBrowser(t)
	b.signUp(t, "ada@example.com")
	value := b.sessionCookie().Value

	// Recently used: no refresh, no Set-Cookie.
	env.clock.Advance(time.Hour)
	if r := b.do(t, "GET", "/v1/me", nil); r.status != 200 || r.setCookie(sessionCookieName) != nil {
		t.Fatalf("early request: %d, cookie %v", r.status, r.setCookie(sessionCookieName))
	}
	// More than a day since the last update: the session slides and the cookie is re-sent.
	env.clock.Advance(24 * time.Hour)
	r := b.do(t, "GET", "/v1/me", nil)
	if c := r.setCookie(sessionCookieName); r.status != 200 || c == nil || c.Value != value || c.MaxAge != 7*24*3600 {
		t.Fatalf("refresh: %d %+v", r.status, c)
	}
	// Used within 7 days of the refresh, so still valid 6 days later...
	env.clock.Advance(6 * 24 * time.Hour)
	if r := b.do(t, "GET", "/v1/me", nil); r.status != 200 {
		t.Fatalf("slid session expired early: %d", r.status)
	}
	// ...but abandoned for longer than the TTL it is dead, and the cookie is cleared.
	env.clock.Advance(8 * 24 * time.Hour)
	r = b.do(t, "GET", "/v1/me", nil)
	if c := r.setCookie(sessionCookieName); r.status != 401 || c == nil || c.MaxAge >= 0 {
		t.Fatalf("expired session: %d %+v", r.status, c)
	}
}

func TestRevokeSessionIsScopedToOwner(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	ada, bob := env.newBrowser(t), env.newBrowser(t)
	ada.signUp(t, "ada@example.com")
	bobSignup := bob.signUp(t, "bob@example.com")
	bobSessionID := bobSignup.obj(t)["session"].(map[string]any)["id"]

	if r := ada.do(t, "POST", "/v1/session-auth/revoke-session", map[string]any{"id": bobSessionID}); r.status != 404 {
		t.Fatalf("ada revoking bob's session = %d, want 404", r.status)
	}
	if r := bob.do(t, "GET", "/v1/me", nil); r.status != 200 {
		t.Fatal("bob was logged out by ada")
	}
	if r := ada.do(t, "POST", "/v1/session-auth/revoke-session", map[string]any{"id": "nonexistent"}); r.status != 404 {
		t.Fatalf("unknown session id = %d, want 404", r.status)
	}
	// Revoking your own current session works like sign-out.
	own := ada.do(t, "GET", "/v1/session-auth/get-session", nil).obj(t)["session"].(map[string]any)["id"]
	if r := ada.do(t, "POST", "/v1/session-auth/revoke-session", map[string]any{"id": own}); r.status != 200 {
		t.Fatalf("revoke own = %d", r.status)
	}
	if r := ada.do(t, "GET", "/v1/me", nil); r.status != 401 {
		t.Fatalf("after revoking own session: %d", r.status)
	}
}

// --- CSRF ---------------------------------------------------------------------------------------

func TestCSRFProtectionForCookieAuth(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.corsOrigins = []string{"https://app.example"} })
	ada := env.newBrowser(t)
	ada.signUp(t, "ada@example.com")

	post := func(hdr ...string) resp {
		return ada.do(t, "POST", "/v1/projects", map[string]string{"name": fmt.Sprint(time.Now().UnixNano())}, hdr...)
	}
	// The attack: a page on another site makes the browser POST to us WITH the victim's cookie.
	// Browsers label such requests; CrossOriginProtection rejects them.
	for name, hdr := range map[string][]string{
		"Sec-Fetch-Site cross-site": {"Sec-Fetch-Site", "cross-site"},
		"Sec-Fetch-Site same-site":  {"Sec-Fetch-Site", "same-site"},
		"foreign Origin":            {"Origin", "https://evil.example"},
	} {
		r := post(hdr...)
		if r.status != http.StatusForbidden || errorCode(t, r) != "forbidden" {
			t.Errorf("%s: %d %s, want 403", name, r.status, r.raw)
		}
	}
	// The project must not have been created by any of the rejected requests.
	if l := ada.do(t, "GET", "/v1/projects", nil); len(l.obj(t)["items"].([]any)) != 0 {
		t.Fatal("a cross-origin request had a side effect")
	}

	// Legitimate requests pass: same-origin browser, non-browser client (no headers at all), and
	// an origin explicitly trusted via the CORS allow-list.
	for name, hdr := range map[string][]string{
		"same-origin Sec-Fetch-Site": {"Sec-Fetch-Site", "same-origin"},
		"same-origin Origin":         {"Origin", env.srv.URL},
		"no headers (curl)":          nil,
		"trusted origin":             {"Origin", "https://app.example"},
	} {
		if r := post(hdr...); r.status != http.StatusCreated {
			t.Errorf("%s: %d %s, want 201", name, r.status, r.raw)
		}
	}

	// Safe methods are never blocked (and must never change state).
	if r := ada.do(t, "GET", "/v1/projects", nil, "Sec-Fetch-Site", "cross-site"); r.status != 200 {
		t.Errorf("cross-site GET = %d", r.status)
	}

	// Credential endpoints are protected too: "login CSRF" (signing the victim into the
	// attacker's account) and forced sign-out.
	if r := env.newBrowser(t).do(t, "POST", "/v1/session-auth/sign-in/email", map[string]string{"email": "ada@example.com", "password": "correct horse"}, "Sec-Fetch-Site", "cross-site"); r.status != 403 {
		t.Errorf("cross-site sign-in = %d, want 403", r.status)
	}
	if r := ada.do(t, "POST", "/v1/session-auth/sign-out", nil, "Sec-Fetch-Site", "cross-site"); r.status != 403 {
		t.Errorf("cross-site sign-out = %d, want 403", r.status)
	}
	if r := ada.do(t, "GET", "/v1/me", nil); r.status != 200 {
		t.Error("a forged sign-out logged the user out")
	}
}

// Bearer tokens are not ambient credentials, so they need no CSRF check: a cross-site header
// cannot make a JWT request any more forgeable than it already is (an attacker cannot add the
// Authorization header from another origin).
func TestBearerAuthIsNotSubjectToCSRFCheck(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	_, token := env.signupAndLogin(t, "ada@example.com")
	r := env.do(t, "POST", "/v1/projects", token, map[string]string{"name": "p"})
	if r.status != 201 {
		t.Fatalf("baseline: %d", r.status)
	}
	req, _ := http.NewRequestWithContext(t.Context(), "POST", env.srv.URL+"/v1/projects", strings.NewReader(`{"name":"q"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != 201 {
		t.Fatalf("bearer request with cross-site header = %d, want 201", res.StatusCode)
	}
}

func TestCORSAllowsCredentialsOnlyForListedOrigins(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.corsOrigins = []string{"https://app.example"} })
	req, _ := http.NewRequestWithContext(t.Context(), "OPTIONS", env.srv.URL+"/v1/session-auth/get-session", nil)
	req.Header.Set("Origin", "https://app.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err := env.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Access-Control-Allow-Origin") != "https://app.example" || res.Header.Get("Access-Control-Allow-Credentials") != "true" {
		t.Fatalf("listed origin: %v", res.Header)
	}

	wild := newTestEnv(t, func(o *envOptions) { o.corsOrigins = []string{"*"} })
	req, _ = http.NewRequestWithContext(t.Context(), "OPTIONS", wild.srv.URL+"/v1/projects", nil)
	req.Header.Set("Origin", "https://anything.example")
	req.Header.Set("Access-Control-Request-Method", "GET")
	res, err = wild.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.Header.Get("Access-Control-Allow-Origin") != "*" || res.Header.Get("Access-Control-Allow-Credentials") != "" {
		t.Fatalf("wildcard must never allow credentials: %v", res.Header)
	}
}

// --- Both modes side by side ------------------------------------------------------------------

func TestJWTAndSessionModesCoexist(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)

	// Account created through the COOKIE flow can log in with the JWT endpoint: one user, one
	// credential account, two ways in.
	web := env.newBrowser(t)
	su := web.signUp(t, "ada@example.com")
	userID := su.obj(t)["user"].(map[string]any)["id"]
	login := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ada@example.com", "password": "correct horse"})
	if login.status != 200 {
		t.Fatalf("JWT login for a session-created account: %d %s", login.status, login.raw)
	}
	token := login.obj(t)["token"].(string)

	// Create with the JWT, read with the cookie, and vice versa.
	pid := env.createProject(t, token, "From JWT")
	if r := web.do(t, "GET", "/v1/projects/"+pid, nil); r.status != 200 || r.obj(t)["owner_id"] != userID {
		t.Fatalf("cookie read of JWT-created project: %d %s", r.status, r.raw)
	}
	if r := web.do(t, "POST", "/v1/projects", map[string]string{"name": "From cookie"}); r.status != 201 {
		t.Fatalf("cookie create: %d", r.status)
	}
	if r := env.do(t, "GET", "/v1/projects", token, nil); len(r.obj(t)["items"].([]any)) != 2 {
		t.Fatalf("JWT list: %s", r.raw)
	}

	// Both credentials on one request: Bearer wins; an invalid Bearer is a 401 even with a valid
	// cookie (no silent fallback).
	cookie := web.sessionCookie()
	call := func(authz string) int {
		req, _ := http.NewRequestWithContext(t.Context(), "GET", env.srv.URL+"/v1/me", nil)
		req.AddCookie(cookie)
		if authz != "" {
			req.Header.Set("Authorization", authz)
		}
		res, err := env.client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if got := call("Bearer " + token); got != 200 {
		t.Errorf("valid Bearer + valid cookie = %d", got)
	}
	if got := call(""); got != 200 {
		t.Errorf("cookie only = %d", got)
	}
	if got := call("Bearer not-a-jwt"); got != 401 {
		t.Errorf("invalid Bearer + valid cookie = %d, want 401", got)
	}

	// A JWT has no server-side session, so the session-management endpoints refuse it.
	if r := env.do(t, "GET", "/v1/session-auth/list-sessions", token, nil); r.status != 401 {
		t.Errorf("list-sessions with a JWT = %d, want 401", r.status)
	}
	// ...but the shared endpoints accept it.
	if r := env.do(t, "POST", "/v1/session-auth/send-verification-email", token, nil); r.status != 200 {
		t.Errorf("send-verification-email with a JWT = %d", r.status)
	}

	// And the reverse: JWT signup creates a user that can use the cookie flow.
	env.do(t, "POST", "/v1/auth/signup", "", map[string]string{"email": "jwt@example.com", "name": "J", "password": "correct horse"})
	if r := env.newBrowser(t).signIn(t, "jwt@example.com", "correct horse"); r.status != 200 {
		t.Fatalf("cookie sign-in for a JWT-created account: %d %s", r.status, r.raw)
	}

	// Statelessness vs revocation, the core tradeoff: signing the cookie session out does not
	// affect the JWT (it stays valid until it expires), which is why the README recommends
	// short JWT lifetimes.
	web.do(t, "POST", "/v1/session-auth/sign-out", nil)
	if r := env.do(t, "GET", "/v1/me", token, nil); r.status != 200 {
		t.Errorf("JWT after cookie sign-out = %d", r.status)
	}
}

// --- Email verification ------------------------------------------------------------------------

func TestEmailVerificationFlow(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	b := env.newBrowser(t)
	b.signUp(t, "ada@example.com")

	m, ok := env.mail.Last()
	if !ok || m.To != "ada@example.com" || !strings.Contains(m.Body, "https://api.example.test/v1/session-auth/verify-email?token=") {
		t.Fatalf("verification email = %+v", m)
	}
	token := env.mailToken(t)

	// A bad token is a 400 and verifies nothing.
	if r := env.do(t, "GET", "/v1/session-auth/verify-email?token=bogus", "", nil); r.status != 400 || errorCode(t, r) != "invalid_token" {
		t.Fatalf("bogus token: %d %s", r.status, r.raw)
	}
	if r := env.do(t, "GET", "/v1/session-auth/verify-email", "", nil); r.status != 400 {
		t.Fatalf("missing token: %d", r.status)
	}

	if r := env.do(t, "GET", "/v1/session-auth/verify-email?token="+token, "", nil); r.status != 200 {
		t.Fatalf("verify: %d %s", r.status, r.raw)
	}
	if u := b.do(t, "GET", "/v1/session-auth/get-session", nil).obj(t)["user"].(map[string]any); u["email_verified"] != true {
		t.Fatalf("user after verify = %v", u)
	}
	// Single use.
	if r := env.do(t, "GET", "/v1/session-auth/verify-email?token="+token, "", nil); r.status != 400 {
		t.Fatalf("reused token: %d", r.status)
	}
	// Already verified: asking again is a no-op without an email.
	n := len(env.mail.Sent())
	if r := b.do(t, "POST", "/v1/session-auth/send-verification-email", nil); r.status != 200 || len(env.mail.Sent()) != n {
		t.Fatalf("resend for verified user: %d, mails %d -> %d", r.status, n, len(env.mail.Sent()))
	}
}

func TestEmailVerificationTokenExpires(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	b := env.newBrowser(t)
	b.signUp(t, "ada@example.com")
	token := env.mailToken(t)

	env.clock.Advance(24*time.Hour + time.Second)
	if r := env.do(t, "GET", "/v1/session-auth/verify-email?token="+token, "", nil); r.status != 400 {
		t.Fatalf("expired token: %d", r.status)
	}
	// A fresh email works (and the old link stays dead even though it is no longer "expired-only").
	if r := b.do(t, "POST", "/v1/session-auth/send-verification-email", nil); r.status != 200 {
		t.Fatalf("resend: %d", r.status)
	}
	fresh := env.mailToken(t)
	if fresh == token {
		t.Fatal("same token reissued")
	}
	if r := env.do(t, "GET", "/v1/session-auth/verify-email?token="+fresh, "", nil); r.status != 200 {
		t.Fatalf("fresh token: %d", r.status)
	}
}

// --- Password reset ------------------------------------------------------------------------------

func TestPasswordResetFlow(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	laptop, phone := env.newBrowser(t), env.newBrowser(t)
	laptop.signUp(t, "ada@example.com")
	phone.signIn(t, "ada@example.com", "correct horse")
	env.signupAndLogin(t, "bob@example.com") // another user, must be unaffected
	bobBrowser := env.newBrowser(t)
	bobBrowser.signIn(t, "bob@example.com", "correct horse")
	mails := len(env.mail.Sent())

	// No user enumeration: the responses for a real and an unknown address are identical.
	strip := func(r resp) string {
		m := r.obj(t)
		return fmt.Sprint(r.status, m)
	}
	unknown := env.do(t, "POST", "/v1/session-auth/forget-password", "", map[string]string{"email": "ghost@example.com"})
	if unknown.status != 200 || len(env.mail.Sent()) != mails {
		t.Fatalf("unknown email: %d, mails sent %d", unknown.status, len(env.mail.Sent())-mails)
	}
	known := env.do(t, "POST", "/v1/session-auth/forget-password", "", map[string]string{"email": "Ada@Example.com"})
	if strip(known) != strip(unknown) {
		t.Fatalf("known and unknown responses differ: %s vs %s", known.raw, unknown.raw)
	}
	if m, _ := env.mail.Last(); len(env.mail.Sent()) != mails+1 || m.To != "ada@example.com" || m.Subject != "Reset your password" {
		t.Fatalf("reset email = %+v", m)
	}
	if r := env.do(t, "POST", "/v1/session-auth/forget-password", "", map[string]string{"email": "not-an-email"}); r.status != 422 {
		t.Fatalf("malformed email = %d", r.status)
	}
	token := env.mailToken(t)

	// A weak password is rejected and does NOT burn the token.
	weak := env.do(t, "POST", "/v1/session-auth/reset-password", "", map[string]string{"token": token, "new_password": "short"})
	if weak.status != 422 || weak.obj(t)["error"].(map[string]any)["fields"].(map[string]any)["new_password"] == nil {
		t.Fatalf("weak password: %d %s", weak.status, weak.raw)
	}
	// A reset token cannot verify an email.
	if r := env.do(t, "GET", "/v1/session-auth/verify-email?token="+token, "", nil); r.status != 400 {
		t.Fatalf("reset token accepted as verification token: %d", r.status)
	}

	r := laptop.do(t, "POST", "/v1/session-auth/reset-password", map[string]string{"token": token, "new_password": "a brand new secret"})
	if r.status != 200 {
		t.Fatalf("reset: %d %s", r.status, r.raw)
	}
	// ALL of Ada's sessions are gone, on every device...
	for name, b := range map[string]*browser{"laptop": laptop, "phone": phone} {
		if r := b.do(t, "GET", "/v1/me", nil); r.status != 401 {
			t.Errorf("%s still signed in after the reset: %d", name, r.status)
		}
	}
	// ...but Bob's are not.
	if r := bobBrowser.do(t, "GET", "/v1/me", nil); r.status != 200 {
		t.Errorf("reset signed out another user: %d", r.status)
	}
	// Old password dead (both modes), new one works (both modes).
	if r := env.newBrowser(t).signIn(t, "ada@example.com", "correct horse"); r.status != 401 {
		t.Errorf("old password after reset = %d", r.status)
	}
	if r := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ada@example.com", "password": "correct horse"}); r.status != 401 {
		t.Errorf("old password via JWT login = %d", r.status)
	}
	if r := env.newBrowser(t).signIn(t, "ada@example.com", "a brand new secret"); r.status != 200 {
		t.Errorf("new password = %d", r.status)
	}
	if r := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "ada@example.com", "password": "a brand new secret"}); r.status != 200 {
		t.Errorf("new password via JWT login = %d", r.status)
	}
	// The stored hash is Argon2id.
	u, _ := env.store.GetUserByEmail(t.Context(), "ada@example.com")
	acc, _ := env.store.GetCredentialAccount(t.Context(), u.ID)
	if acc.Password == nil || !strings.HasPrefix(*acc.Password, "$argon2id$v=19$") {
		t.Fatalf("stored password = %v", acc.Password)
	}
	// Single use.
	again := env.do(t, "POST", "/v1/session-auth/reset-password", "", map[string]string{"token": token, "new_password": "yet another secret"})
	if again.status != 400 || errorCode(t, again) != "invalid_token" {
		t.Fatalf("token reuse: %d %s", again.status, again.raw)
	}
}

func TestPasswordResetTokenExpires(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t)
	env.newBrowser(t).signUp(t, "ada@example.com")
	env.do(t, "POST", "/v1/session-auth/forget-password", "", map[string]string{"email": "ada@example.com"})
	token := env.mailToken(t)

	env.clock.Advance(59 * time.Minute)
	// Still valid at 59 minutes (verified by NOT consuming it: use a weak password probe).
	if r := env.do(t, "POST", "/v1/session-auth/reset-password", "", map[string]string{"token": token, "new_password": "x"}); r.status != 422 {
		t.Fatalf("probe: %d", r.status)
	}
	env.clock.Advance(2 * time.Minute) // 61 minutes after issue
	r := env.do(t, "POST", "/v1/session-auth/reset-password", "", map[string]string{"token": token, "new_password": "a brand new secret"})
	if r.status != 400 || errorCode(t, r) != "invalid_token" {
		t.Fatalf("expired reset token: %d %s", r.status, r.raw)
	}
	if r := env.newBrowser(t).signIn(t, "ada@example.com", "correct horse"); r.status != 200 {
		t.Fatalf("password changed by an expired token: %d", r.status)
	}
}

// --- Strict rate limit -----------------------------------------------------------------------------

func TestCredentialEndpointsHaveAStricterRateLimit(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.authRateRPS, o.authRate = 0.001, 3 })
	b := env.newBrowser(t)
	var codes []int
	for range 5 {
		codes = append(codes, b.signIn(t, "ada@example.com", "guess guess").status)
	}
	if fmt.Sprint(codes) != "[401 401 401 429 429]" {
		t.Fatalf("sign-in statuses = %v, want three 401s then 429s", codes)
	}
	// The general limit is separate: ordinary endpoints are unaffected.
	if r := env.do(t, "GET", "/healthz", "", nil); r.status != 200 {
		t.Fatalf("/healthz = %d", r.status)
	}
	// JWT login shares the strict bucket (same attack surface).
	if r := env.do(t, "POST", "/v1/auth/login", "", map[string]string{"email": "a@example.com", "password": "x"}); r.status != 429 {
		t.Fatalf("JWT login = %d, want 429", r.status)
	}
}

// --- GitHub OAuth -------------------------------------------------------------------------------------

func ghUser(id int64, login string, emails ...githubtest.Email) githubtest.User {
	return githubtest.User{ID: id, Login: login, Name: "", AvatarURL: "https://avatars.example/" + login, Emails: emails}
}

var verifiedPrimary = func(addr string) githubtest.Email {
	return githubtest.Email{Email: addr, Primary: true, Verified: true}
}

// startGitHub performs step 1 and returns the state, plus the Location (GitHub URL).
func startGitHub(t *testing.T, b *browser) (state, location string, r resp) {
	t.Helper()
	r = b.do(t, "GET", "/v1/session-auth/oauth/github", nil)
	if r.status != http.StatusFound {
		t.Fatalf("oauth start: %d %s", r.status, r.raw)
	}
	loc, err := url.Parse(r.header.Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	return loc.Query().Get("state"), loc.String(), r
}

// approveAtGitHub plays the user clicking "Authorize" at the fake GitHub and returns the code
// GitHub would redirect back with.
func approveAtGitHub(t *testing.T, env *testEnv, location string) (code, state string) {
	t.Helper()
	c := *env.github.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Get(location)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	back, err := res.Location()
	if err != nil {
		t.Fatalf("GitHub did not redirect (%d): %v", res.StatusCode, err)
	}
	return back.Query().Get("code"), back.Query().Get("state")
}

func callbackPath(code, state string) string {
	return "/v1/session-auth/oauth/github/callback?" + url.Values{"code": {code}, "state": {state}}.Encode()
}

func TestGitHubOAuthCreatesUser(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.withGitHub = true })
	env.github.SetUser(ghUser(4242, "octo", verifiedPrimary("Octo@Example.com")))
	b := env.newBrowser(t)

	state, location, start := startGitHub(t, b)
	sc := start.setCookie(oauthCookieName)
	if sc == nil || !sc.HttpOnly || sc.Path != oauthCookiePath || sc.MaxAge != 600 || sc.SameSite != http.SameSiteLaxMode {
		t.Fatalf("oauth state cookie = %+v", sc)
	}
	if !strings.HasPrefix(location, env.github.URL+"/login/oauth/authorize") || state == "" || !strings.Contains(location, "code_challenge_method=S256") {
		t.Fatalf("redirect = %s", location)
	}

	code, backState := approveAtGitHub(t, env, location)
	if backState != state {
		t.Fatalf("GitHub returned state %q, want %q", backState, state)
	}
	r := b.do(t, "GET", callbackPath(code, backState), nil)
	if r.status != 200 {
		t.Fatalf("callback: %d %s", r.status, r.raw)
	}
	u := r.obj(t)["user"].(map[string]any)
	if u["email"] != "octo@example.com" || u["email_verified"] != true || u["name"] != "octo" || u["image"] != "https://avatars.example/octo" {
		t.Fatalf("user = %v", u)
	}
	if r.setCookie(sessionCookieName) == nil {
		t.Fatal("callback did not set the session cookie")
	}
	if cc := r.setCookie(oauthCookieName); cc == nil || cc.MaxAge >= 0 {
		t.Fatalf("state cookie not cleared: %+v", cc)
	}
	if me := b.do(t, "GET", "/v1/me", nil); me.status != 200 || me.obj(t)["email"] != "octo@example.com" {
		t.Fatalf("/v1/me after oauth: %d %s", me.status, me.raw)
	}

	// Signing in again with the same GitHub identity reuses the same user.
	b2 := env.newBrowser(t)
	_, loc2, _ := startGitHub(t, b2)
	code2, st2 := approveAtGitHub(t, env, loc2)
	r2 := b2.do(t, "GET", callbackPath(code2, st2), nil)
	if r2.status != 200 || r2.obj(t)["user"].(map[string]any)["id"] != u["id"] {
		t.Fatalf("second sign-in: %d %s", r2.status, r2.raw)
	}

	// A GitHub-only account has no password: password sign-in fails, but "forget password" can
	// give it one.
	if r := env.newBrowser(t).signIn(t, "octo@example.com", "guess guess"); r.status != 401 {
		t.Fatalf("password sign-in for a GitHub-only user = %d", r.status)
	}
	env.do(t, "POST", "/v1/session-auth/forget-password", "", map[string]string{"email": "octo@example.com"})
	if r := env.do(t, "POST", "/v1/session-auth/reset-password", "", map[string]string{"token": env.mailToken(t), "new_password": "now i have one"}); r.status != 200 {
		t.Fatalf("reset for GitHub-only user: %d %s", r.status, r.raw)
	}
	if r := env.newBrowser(t).signIn(t, "octo@example.com", "now i have one"); r.status != 200 {
		t.Fatalf("sign-in with the new password: %d", r.status)
	}
}

func TestGitHubOAuthRejectsBadState(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.withGitHub = true })
	env.github.SetUser(ghUser(1, "octo", verifiedPrimary("octo@example.com")))

	// Each case gets a fresh browser that really started the flow (so it has a state cookie) and
	// a genuine code from GitHub; only the callback URL is manipulated.
	cases := map[string]func(code, state string) string{
		// An attacker feeds the victim's browser a callback URL carrying THEIR state.
		"wrong state":  func(code, _ string) string { return callbackPath(code, "attacker-chosen-state") },
		"empty state":  func(code, _ string) string { return callbackPath(code, "") },
		"missing code": func(_, state string) string { return callbackPath("", state) },
		"provider deny": func(_, state string) string {
			return "/v1/session-auth/oauth/github/callback?error=access_denied&state=" + state
		},
		"forged code": func(_, state string) string { return callbackPath("code-that-github-never-issued", state) },
	}
	for name, mk := range cases {
		b := env.newBrowser(t)
		_, location, _ := startGitHub(t, b)
		code, state := approveAtGitHub(t, env, location)
		r := b.do(t, "GET", mk(code, state), nil)
		want := 400
		if name == "forged code" {
			want = 502 // state was fine; GitHub itself refused the code
		}
		if r.status != want {
			t.Errorf("%s: %d %s, want %d", name, r.status, r.raw, want)
		}
		if b.sessionCookie() != nil {
			t.Errorf("%s: a session was created", name)
		}
	}

	// The state cookie is single-use: replaying the legitimate callback fails.
	good := env.newBrowser(t)
	_, loc, _ := startGitHub(t, good)
	code, state := approveAtGitHub(t, env, loc)
	if r := good.do(t, "GET", callbackPath(code, state), nil); r.status != 200 {
		t.Fatalf("legit callback: %d %s", r.status, r.raw)
	}
	if r := good.do(t, "GET", callbackPath(code, state), nil); r.status != 400 {
		t.Fatalf("replayed callback: %d, want 400 (state cookie consumed)", r.status)
	}

	// A browser that never started the flow has no state cookie at all.
	_, loc, _ = startGitHub(t, env.newBrowser(t))
	code, state = approveAtGitHub(t, env, loc)
	if r := env.newBrowser(t).do(t, "GET", callbackPath(code, state), nil); r.status != 400 {
		t.Fatalf("callback without state cookie: %d", r.status)
	}
}

func TestGitHubOAuthAccountLinking(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t, func(o *envOptions) { o.withGitHub = true })
	signInWithGitHub := func() resp {
		b := env.newBrowser(t)
		_, loc, _ := startGitHub(t, b)
		code, st := approveAtGitHub(t, env, loc)
		return b.do(t, "GET", callbackPath(code, st), nil)
	}

	// 1. A local user whose email is VERIFIED: GitHub is linked to the same user.
	local := env.newBrowser(t)
	su := local.signUp(t, "ada@example.com")
	localID := su.obj(t)["user"].(map[string]any)["id"]
	env.do(t, "GET", "/v1/session-auth/verify-email?token="+env.mailToken(t), "", nil)
	env.github.SetUser(ghUser(100, "ada-gh", verifiedPrimary("ADA@example.com")))
	r := signInWithGitHub()
	if r.status != 200 || r.obj(t)["user"].(map[string]any)["id"] != localID {
		t.Fatalf("link to verified local user: %d %s", r.status, r.raw)
	}
	// The password still works: linking added a way in, it did not replace one.
	if r := env.newBrowser(t).signIn(t, "ada@example.com", "correct horse"); r.status != 200 {
		t.Fatalf("password sign-in after linking: %d", r.status)
	}

	// 2. A local user whose email is NOT verified is never merged (pre-hijacking defence).
	env.newBrowser(t).signUp(t, "victim@example.com")
	env.github.SetUser(ghUser(200, "victim-gh", verifiedPrimary("victim@example.com")))
	if r := signInWithGitHub(); r.status != 409 {
		t.Fatalf("unverified local user: %d %s, want 409", r.status, r.raw)
	}

	// 3. GitHub account without a verified primary email: refused.
	env.github.SetUser(ghUser(300, "noemail",
		githubtest.Email{Email: "x@example.com", Primary: true, Verified: false},
		githubtest.Email{Email: "y@example.com", Verified: true}))
	if r := signInWithGitHub(); r.status != 422 {
		t.Fatalf("no verified primary email: %d %s, want 422", r.status, r.raw)
	}
}

func TestGitHubOAuthDisabledWithoutCredentials(t *testing.T) {
	t.Parallel()
	env := newTestEnv(t) // no GitHub configured
	b := env.newBrowser(t)
	for _, path := range []string{"/v1/session-auth/oauth/github", "/v1/session-auth/oauth/github/callback?code=x&state=y"} {
		r := b.do(t, "GET", path, nil)
		if r.status != 404 || errorCode(t, r) != "not_found" {
			t.Errorf("%s = %d %s, want 404 JSON", path, r.status, r.raw)
		}
	}
}
