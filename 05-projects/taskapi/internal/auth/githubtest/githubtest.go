// Package githubtest is a minimal fake of GitHub's OAuth and REST endpoints for tests. It is a
// real HTTP server (httptest), so the production client code (auth.GitHub) runs unchanged; only
// its three base URLs are redirected here. It also VERIFIES PKCE, so a test fails if the client
// forgets to send the challenge or the verifier.
//
// Endpoints: GET /login/oauth/authorize, POST /login/oauth/access_token, GET /api/user,
// GET /api/user/emails.
package githubtest

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
)

const (
	ClientID     = "test-client-id"
	ClientSecret = "test-client-secret"
)

// Email is one entry of GET /user/emails.
type Email struct {
	Email    string `json:"email"`
	Primary  bool   `json:"primary"`
	Verified bool   `json:"verified"`
}

// User is the GitHub account the fake will report.
type User struct {
	ID        int64
	Login     string
	Name      string
	AvatarURL string
	Emails    []Email
}

// Server is the fake. Change User (via SetUser) between flows to simulate different people.
type Server struct {
	*httptest.Server

	mu     sync.Mutex
	user   User
	codes  map[string]string // authorization code -> PKCE challenge
	tokens map[string]bool   // issued access tokens
	n      int
}

// New starts the fake and stops it when the test ends.
func New(tb testing.TB) *Server {
	tb.Helper()
	s := &Server{codes: map[string]string{}, tokens: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /login/oauth/authorize", s.authorize)
	mux.HandleFunc("POST /login/oauth/access_token", s.token)
	mux.HandleFunc("GET /api/user", s.apiUser)
	mux.HandleFunc("GET /api/user/emails", s.apiEmails)
	s.Server = httptest.NewTestServer(tb, mux)
	return s
}

// SetUser sets the account the fake reports from now on.
func (s *Server) SetUser(u User) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.user = u
}

// Config returns an auth.GitHubConfig whose URLs point at the fake.
func (s *Server) Config(redirectURL string) auth.GitHubConfig {
	return auth.GitHubConfig{
		ClientID: ClientID, ClientSecret: ClientSecret, RedirectURL: redirectURL,
		AuthURL: s.URL + "/login/oauth/authorize", TokenURL: s.URL + "/login/oauth/access_token",
		APIBaseURL: s.URL + "/api", HTTPClient: s.Client(),
	}
}

// authorize plays "the user logged in and clicked Approve": it issues a code bound to the PKCE
// challenge and redirects to redirect_uri with ?code=...&state=... exactly like GitHub.
func (s *Server) authorize(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("client_id") != ClientID || q.Get("code_challenge") == "" || q.Get("code_challenge_method") != "S256" || q.Get("state") == "" {
		http.Error(w, "bad authorize request", http.StatusBadRequest)
		return
	}
	redirect, err := url.Parse(q.Get("redirect_uri"))
	if err != nil || redirect.Scheme == "" {
		http.Error(w, "bad redirect_uri", http.StatusBadRequest)
		return
	}
	s.mu.Lock()
	s.n++
	code := fmt.Sprintf("code-%d", s.n)
	s.codes[code] = q.Get("code_challenge")
	s.mu.Unlock()

	rq := redirect.Query()
	rq.Set("code", code)
	rq.Set("state", q.Get("state"))
	redirect.RawQuery = rq.Encode()
	http.Redirect(w, r, redirect.String(), http.StatusFound)
}

func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	code := r.PostForm.Get("code")
	s.mu.Lock()
	challenge, ok := s.codes[code]
	delete(s.codes, code) // codes are single-use
	s.mu.Unlock()

	sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
	switch {
	case r.PostForm.Get("client_id") != ClientID || r.PostForm.Get("client_secret") != ClientSecret:
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "incorrect_client_credentials"})
	case !ok:
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code"}) // GitHub answers 200 + error
	case base64.RawURLEncoding.EncodeToString(sum[:]) != challenge:
		writeJSON(w, http.StatusOK, map[string]string{"error": "bad_verification_code", "error_description": "PKCE verifier mismatch"})
	default:
		s.mu.Lock()
		s.n++
		tok := fmt.Sprintf("gho_fake_%d", s.n)
		s.tokens[tok] = true
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]string{"access_token": tok, "token_type": "bearer", "scope": "read:user,user:email"})
	}
}

func (s *Server) authed(w http.ResponseWriter, r *http.Request) (User, bool) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.tokens[tok] {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"message": "Bad credentials"})
		return User{}, false
	}
	return s.user, true
}

func (s *Server) apiUser(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"id": u.ID, "login": u.Login, "name": u.Name, "avatar_url": u.AvatarURL})
	}
}

func (s *Server) apiEmails(w http.ResponseWriter, r *http.Request) {
	if u, ok := s.authed(w, r); ok {
		writeJSON(w, http.StatusOK, u.Emails)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
