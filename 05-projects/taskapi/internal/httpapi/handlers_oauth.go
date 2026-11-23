package httpapi

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// "Sign in with GitHub". Both routes answer 404 unless GitHub credentials are configured, so a
// deployment without OAuth does not even advertise the feature. See auth.GitHub for the flow.

// githubStart (step 1) sends the browser to GitHub with state and a PKCE challenge, and remembers
// state + verifier in a short-lived HttpOnly cookie.
func (s *server) githubStart(w http.ResponseWriter, r *http.Request) error {
	if s.GitHub == nil {
		return domain.ErrNotFound
	}
	// `state` is an unguessable value tying the callback to THIS browser's request; it is the
	// OAuth CSRF defence. auth.NewToken is just "32 random bytes, URL-safe"; the hash is unused.
	state, _, err := auth.NewToken()
	if err != nil {
		return err
	}
	verifier := auth.NewPKCEVerifier()
	s.cookies.setOAuthState(w, state, verifier)
	http.Redirect(w, r, s.GitHub.AuthCodeURL(state, verifier), http.StatusFound)
	return nil
}

// githubCallback (step 3 onwards) validates state, completes the exchange, signs the user in and
// sets the session cookie. A browser app would redirect to the SPA here; we return the same JSON
// as sign-in so the flow is easy to inspect and test.
func (s *server) githubCallback(w http.ResponseWriter, r *http.Request) error {
	if s.GitHub == nil {
		return domain.ErrNotFound
	}
	// The state cookie is single-use: clear it no matter how this request ends.
	s.cookies.clearOAuthState(w)

	c, err := r.Cookie(oauthCookieName)
	if err != nil {
		return &badRequestError{"missing or expired oauth state; start again from /v1/session-auth/oauth/github"}
	}
	wantState, verifier, ok := strings.Cut(c.Value, ".")
	q := r.URL.Query()
	gotState := q.Get("state")
	// Constant-time compare: state is a secret for the length of the flow.
	if !ok || wantState == "" || verifier == "" || subtle.ConstantTimeCompare([]byte(gotState), []byte(wantState)) != 1 {
		return &badRequestError{"invalid oauth state"}
	}
	if q.Get("error") != "" { // the user clicked "Cancel" at GitHub
		return &badRequestError{"authorization was denied"}
	}
	code := q.Get("code")
	if code == "" {
		return &badRequestError{"missing authorization code"}
	}

	profile, err := s.GitHub.Profile(r.Context(), code, verifier)
	if err != nil {
		// GitHub rejected the code (replayed/expired) or is down. The cause stays in the log.
		s.Logger.WarnContext(r.Context(), "github sign-in failed", "error", err, "request_id", requestIDFrom(r.Context()))
		return errBadGateway
	}
	res, err := s.SessionAuth.SignInWithOAuth(r.Context(), profile, clientMeta(r))
	if err != nil {
		return err
	}
	s.cookies.set(w, res.Token)
	writeJSON(w, http.StatusOK, sessionBody{res.User, res.Session})
	return nil
}
