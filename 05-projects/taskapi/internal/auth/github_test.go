package auth_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"golang.org/x/oauth2"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth/githubtest"
)

// authorize plays the browser: follow the authorization URL to the fake GitHub and return the
// code it redirects back with.
func authorize(t *testing.T, fake *githubtest.Server, authURL string) (code, state string) {
	t.Helper()
	// Do not follow the redirect to our (non-existent) callback; read it instead.
	c := *fake.Client()
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, err := c.Get(authURL)
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	loc, err := res.Location()
	if err != nil {
		t.Fatalf("authorize did not redirect (status %d): %v", res.StatusCode, err)
	}
	return loc.Query().Get("code"), loc.Query().Get("state")
}

func TestGitHubFlowWithPKCE(t *testing.T) {
	t.Parallel()
	fake := githubtest.New(t)
	fake.SetUser(githubtest.User{ID: 4242, Login: "octo", AvatarURL: "https://avatars.example/4242", Emails: []githubtest.Email{
		{Email: "secondary@example.com", Verified: true},
		{Email: "Octo@Example.com", Primary: true, Verified: true},
	}})
	gh, err := auth.NewGitHub(fake.Config("http://localhost/callback"))
	if err != nil {
		t.Fatal(err)
	}

	verifier := oauth2.GenerateVerifier()
	authURL := gh.AuthCodeURL("state-123", verifier)
	u, _ := url.Parse(authURL)
	q := u.Query()
	if q.Get("state") != "state-123" || q.Get("code_challenge_method") != "S256" || q.Get("code_challenge") == "" || q.Get("code_challenge") == verifier {
		t.Fatalf("auth URL lacks state or S256 challenge: %s", authURL)
	}
	if !strings.Contains(q.Get("scope"), "user:email") {
		t.Fatalf("scope = %q", q.Get("scope"))
	}

	code, state := authorize(t, fake, authURL)
	if state != "state-123" {
		t.Fatalf("state = %q", state)
	}
	p, err := gh.Profile(t.Context(), code, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if p.Provider != "github" || p.AccountID != "4242" || p.Email != "Octo@Example.com" || !p.EmailVerified || p.Name != "octo" || p.Image == "" || p.AccessToken == "" {
		t.Fatalf("profile = %+v", p)
	}

	// The code is single-use.
	if _, err := gh.Profile(t.Context(), code, verifier); err == nil {
		t.Fatal("authorization code was accepted twice")
	}
}

func TestGitHubWrongVerifierRejected(t *testing.T) {
	t.Parallel()
	fake := githubtest.New(t)
	fake.SetUser(githubtest.User{ID: 1, Login: "x", Emails: []githubtest.Email{{Email: "x@example.com", Primary: true, Verified: true}}})
	gh, _ := auth.NewGitHub(fake.Config("http://localhost/callback"))

	code, _ := authorize(t, fake, gh.AuthCodeURL("s", oauth2.GenerateVerifier()))
	// An attacker who stole the code but not the verifier cannot redeem it.
	if _, err := gh.Profile(t.Context(), code, oauth2.GenerateVerifier()); err == nil {
		t.Fatal("code redeemed with the wrong PKCE verifier")
	}
}

func TestGitHubNoVerifiedPrimaryEmail(t *testing.T) {
	t.Parallel()
	fake := githubtest.New(t)
	fake.SetUser(githubtest.User{ID: 2, Login: "y", Name: "Why", Emails: []githubtest.Email{
		{Email: "primary@example.com", Primary: true, Verified: false},
		{Email: "other@example.com", Verified: true},
	}})
	gh, _ := auth.NewGitHub(fake.Config("http://localhost/callback"))
	v := oauth2.GenerateVerifier()
	code, _ := authorize(t, fake, gh.AuthCodeURL("s", v))
	p, err := gh.Profile(t.Context(), code, v)
	if err != nil {
		t.Fatal(err)
	}
	if p.EmailVerified || p.Email != "" || p.Name != "Why" {
		t.Fatalf("profile = %+v; an unverified primary email must not be reported as verified", p)
	}
}

func TestNewGitHubValidation(t *testing.T) {
	t.Parallel()
	if _, err := auth.NewGitHub(auth.GitHubConfig{ClientID: "x"}); err == nil {
		t.Fatal("incomplete config accepted")
	}
}
