package service

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/mailer"
)

// testClock is a mutable time source shared by the service under test.
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

type sessionEnv struct {
	svc   *SessionAuthService
	store *fakeStore
	mail  *mailer.Memory
	clock *testClock
}

func newSessionEnv(t *testing.T) *sessionEnv {
	t.Helper()
	store := newFakeStore()
	authSvc, _ := newAuthSvc(t, store)
	clock := &testClock{t: fixedNow}
	mail := &mailer.Memory{}
	svc, err := NewSessionAuthService(SessionAuthDeps{
		Auth: authSvc, Users: store, Accounts: store, Sessions: store, Verifications: store,
		Hasher: &fakeHasher{}, Mailer: mail,
		Config: SessionConfig{PublicURL: "https://api.example.test/", Now: clock.Now},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sessionEnv{svc: svc, store: store, mail: mail, clock: clock}
}

var meta = ClientMeta{IP: "203.0.113.7", UserAgent: "test-agent/1.0"}

func (e *sessionEnv) signUp(t *testing.T, email string) SessionResult {
	t.Helper()
	res, err := e.svc.SignUp(t.Context(), domain.SignupInput{Email: email, Name: "Ada", Password: "correct horse"}, meta)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

var tokenRe = regexp.MustCompile(`token=([A-Za-z0-9_-]+)`)

// lastToken extracts the token from the most recent email, the way a user would click the link.
func (e *sessionEnv) lastToken(t *testing.T) string {
	t.Helper()
	m, ok := e.mail.Last()
	if !ok {
		t.Fatal("no email was sent")
	}
	g := tokenRe.FindStringSubmatch(m.Body)
	if g == nil {
		t.Fatalf("no token in email:\n%s", m.Body)
	}
	return g[1]
}

func TestSessionSignUpAndSignIn(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()

	res := e.signUp(t, "Ada@Example.com")
	if res.User.Email != "ada@example.com" || res.Token == "" {
		t.Fatalf("signup = %+v", res)
	}
	// The database stores the HASH of the token, never the token.
	stored, err := e.store.GetSessionByTokenHash(ctx, auth.HashToken(res.Token))
	if err != nil || stored.ID != res.Session.ID || stored.TokenHash == res.Token {
		t.Fatalf("stored session = %+v, %v", stored, err)
	}
	for _, s := range e.store.sessions {
		if s.TokenHash == res.Token {
			t.Fatal("raw token found in the session table")
		}
	}
	if want := fixedNow.Add(7 * 24 * time.Hour); !res.Session.ExpiresAt.Equal(want) {
		t.Fatalf("expires at %v, want %v (default 7 days)", res.Session.ExpiresAt, want)
	}
	if res.Session.IPAddress != meta.IP || res.Session.UserAgent != meta.UserAgent {
		t.Fatalf("metadata = %+v", res.Session)
	}
	// Verification email went out, with the public URL (trailing slash trimmed).
	m, ok := e.mail.Last()
	if !ok || m.To != "ada@example.com" || !strings.Contains(m.Body, "https://api.example.test/v1/session-auth/verify-email?token=") {
		t.Fatalf("verification email = %+v", m)
	}

	// Sign in: right password makes a second, independent session.
	in, err := e.svc.SignIn(ctx, "ada@example.com", "correct horse", meta)
	if err != nil || in.Token == res.Token || in.Session.ID == res.Session.ID {
		t.Fatalf("signin = %+v, %v", in, err)
	}
	// Wrong password and unknown email: same error.
	for _, c := range [][2]string{{"ada@example.com", "wrong"}, {"ghost@example.com", "correct horse"}} {
		if _, err := e.svc.SignIn(ctx, c[0], c[1], meta); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("SignIn(%v) = %v, want ErrUnauthorized", c, err)
		}
	}
	// Duplicate signup.
	if _, err := e.svc.SignUp(ctx, domain.SignupInput{Email: "ada@example.com", Name: "x", Password: "correct horse"}, meta); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate signup: %v", err)
	}
	// A failing mailer must not undo a signup.
	e.mail.Err = errors.New("smtp down")
	if _, err := e.svc.SignUp(ctx, domain.SignupInput{Email: "bob@example.com", Name: "Bob", Password: "correct horse"}, meta); err != nil {
		t.Errorf("signup failed because the mailer failed: %v", err)
	}
}

func TestUserAgentIsTruncated(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	res, err := e.svc.SignUp(t.Context(), domain.SignupInput{Email: "a@example.com", Name: "A", Password: "correct horse"},
		ClientMeta{UserAgent: strings.Repeat("é", 1000)})
	if err != nil {
		t.Fatal(err)
	}
	if n := len(res.Session.UserAgent); n > maxUserAgentBytes || n < maxUserAgentBytes-1 {
		t.Fatalf("user agent length %d", n)
	}
	if !strings.HasSuffix(res.Session.UserAgent, "é") {
		t.Fatal("truncation split a UTF-8 character")
	}
}

func TestSessionAuthenticateExpiryAndSlidingRefresh(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()
	res := e.signUp(t, "ada@example.com")

	// Garbage tokens.
	for _, tok := range []string{"", "nope", strings.Repeat("A", 43)} {
		if _, err := e.svc.Authenticate(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("token %q: %v", tok, err)
		}
	}

	// Within the update age: valid, expiry unchanged, no write.
	e.clock.Advance(2 * time.Hour)
	st, err := e.svc.Authenticate(ctx, res.Token)
	if err != nil || st.Refreshed || !st.Session.ExpiresAt.Equal(res.Session.ExpiresAt) {
		t.Fatalf("early auth = %+v, %v", st, err)
	}

	// 25 hours after the last update (> UpdateAge of 24h): expiry slides to now+7d.
	e.clock.Advance(23 * time.Hour)
	st, err = e.svc.Authenticate(ctx, res.Token)
	if err != nil || !st.Refreshed {
		t.Fatalf("late auth = %+v, %v", st, err)
	}
	wantExp := e.clock.Now().Add(7 * 24 * time.Hour)
	if !st.Session.ExpiresAt.Equal(wantExp) {
		t.Fatalf("slid to %v, want %v", st.Session.ExpiresAt, wantExp)
	}
	if stored, _ := e.store.GetSessionByTokenHash(ctx, auth.HashToken(res.Token)); !stored.ExpiresAt.Equal(wantExp) {
		t.Fatalf("slide not persisted: %v", stored.ExpiresAt)
	}
	// Immediately after, it is fresh again: no second refresh.
	if st, _ = e.svc.Authenticate(ctx, res.Token); st.Refreshed {
		t.Fatal("refreshed twice in a row")
	}

	// Abandon it: after TTL without use the session is dead and its row is deleted on lookup.
	e.clock.Advance(7*24*time.Hour + time.Second)
	if _, err := e.svc.Authenticate(ctx, res.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("expired session accepted: %v", err)
	}
	if _, err := e.store.GetSessionByTokenHash(ctx, auth.HashToken(res.Token)); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired session not deleted lazily: %v", err)
	}
}

func TestSessionSignOutListRevoke(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()
	a1 := e.signUp(t, "ada@example.com")
	a2, _ := e.svc.SignIn(ctx, "ada@example.com", "correct horse", meta)
	a3, _ := e.svc.SignIn(ctx, "ada@example.com", "correct horse", meta)
	bob := e.signUp(t, "bob@example.com")

	list, err := e.svc.ListSessions(ctx, a1.User.ID)
	if err != nil || len(list) != 3 {
		t.Fatalf("list = %d, %v", len(list), err)
	}

	// Cannot revoke someone else's session; it looks nonexistent.
	if err := e.svc.RevokeSession(ctx, a1.User.ID, bob.Session.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("revoking bob's session as ada: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, bob.Token); err != nil {
		t.Fatalf("bob was logged out by ada: %v", err)
	}
	if err := e.svc.RevokeSession(ctx, a1.User.ID, a2.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, a2.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("revoked session still works")
	}

	if err := e.svc.RevokeOtherSessions(ctx, a1.User.ID, a1.Session.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, a3.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("other session survived revoke-others")
	}
	if _, err := e.svc.Authenticate(ctx, a1.Token); err != nil {
		t.Fatalf("current session was revoked: %v", err)
	}
	if _, err := e.svc.Authenticate(ctx, bob.Token); err != nil {
		t.Fatalf("revoke-others touched another user: %v", err)
	}

	// Sign out is idempotent.
	for range 2 {
		if err := e.svc.SignOut(ctx, a1.Token); err != nil {
			t.Fatal(err)
		}
	}
	if err := e.svc.SignOut(ctx, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := e.svc.Authenticate(ctx, a1.Token); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("signed-out session still works")
	}
}

func TestVerifyEmail(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()
	res := e.signUp(t, "ada@example.com")
	token := e.lastToken(t)

	if err := e.svc.VerifyEmail(ctx, token); err != nil {
		t.Fatal(err)
	}
	if u, _ := e.store.GetUserByID(ctx, res.User.ID); !u.EmailVerified {
		t.Fatal("email not marked verified")
	}
	// Single use.
	if err := e.svc.VerifyEmail(ctx, token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("second use: %v", err)
	}
	for _, bad := range []string{"", "bogus"} {
		if err := e.svc.VerifyEmail(ctx, bad); !errors.Is(err, domain.ErrInvalidToken) {
			t.Errorf("token %q: %v", bad, err)
		}
	}

	// Already verified: sending again is a silent no-op.
	n := len(e.mail.Sent())
	if err := e.svc.SendVerificationEmail(ctx, res.User.ID); err != nil || len(e.mail.Sent()) != n {
		t.Fatalf("verified user got another email (%v)", err)
	}
}

func TestVerifyEmailExpiryAndReissue(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()
	res := e.signUp(t, "ada@example.com")
	first := e.lastToken(t)

	// Asking for a new email invalidates the old link.
	if err := e.svc.SendVerificationEmail(ctx, res.User.ID); err != nil {
		t.Fatal(err)
	}
	second := e.lastToken(t)
	if first == second {
		t.Fatal("same token issued twice")
	}
	if err := e.svc.VerifyEmail(ctx, first); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("superseded token: %v", err)
	}

	// Expires after 24 hours.
	e.clock.Advance(24*time.Hour + time.Second)
	if err := e.svc.VerifyEmail(ctx, second); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expired token: %v", err)
	}
}

func TestPasswordResetFlow(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()
	res := e.signUp(t, "ada@example.com")
	other, _ := e.svc.SignIn(ctx, "ada@example.com", "correct horse", meta)

	before := len(e.mail.Sent())

	// Unknown email: success, no email, no error. Malformed: validation error.
	if err := e.svc.ForgetPassword(ctx, "ghost@example.com"); err != nil || len(e.mail.Sent()) != before {
		t.Fatalf("unknown email: err=%v mails=%d", err, len(e.mail.Sent())-before)
	}
	if err := e.svc.ForgetPassword(ctx, "not-an-email"); err == nil {
		t.Fatal("malformed email accepted")
	}

	if err := e.svc.ForgetPassword(ctx, " ADA@example.com "); err != nil {
		t.Fatal(err)
	}
	m, _ := e.mail.Last()
	if m.Subject != "Reset your password" || !strings.Contains(m.Body, "/reset-password?token=") {
		t.Fatalf("reset email = %+v", m)
	}
	token := e.lastToken(t)

	// A reset token is not an email-verification token.
	if err := e.svc.VerifyEmail(ctx, token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("reset token used as verification token: %v", err)
	}
	// A weak new password is rejected WITHOUT burning the token.
	var ve *domain.ValidationError
	if err := e.svc.ResetPassword(ctx, token, "short"); !errors.As(err, &ve) || ve.Fields["new_password"] == "" {
		t.Fatalf("weak password: %v", err)
	}

	if err := e.svc.ResetPassword(ctx, token, "brand new secret"); err != nil {
		t.Fatalf("reset: %v", err)
	}
	// All sessions are gone; the old password is dead; the new one works.
	for _, tok := range []string{res.Token, other.Token} {
		if _, err := e.svc.Authenticate(ctx, tok); !errors.Is(err, domain.ErrUnauthorized) {
			t.Fatalf("session survived the password reset: %v", err)
		}
	}
	if _, err := e.svc.SignIn(ctx, "ada@example.com", "correct horse", meta); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal("old password still works")
	}
	if _, err := e.svc.SignIn(ctx, "ada@example.com", "brand new secret", meta); err != nil {
		t.Fatalf("new password rejected: %v", err)
	}
	// Single use.
	if err := e.svc.ResetPassword(ctx, token, "another secret!"); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("token reused: %v", err)
	}
}

func TestPasswordResetTokenExpiresAfterAnHour(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	e.signUp(t, "ada@example.com")
	if err := e.svc.ForgetPassword(t.Context(), "ada@example.com"); err != nil {
		t.Fatal(err)
	}
	token := e.lastToken(t)
	e.clock.Advance(time.Hour + time.Second)
	if err := e.svc.ResetPassword(t.Context(), token, "brand new secret"); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("expired reset token: %v", err)
	}
}

func TestSendVerificationMailFailureIsReported(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	res := e.signUp(t, "ada@example.com")
	e.mail.Err = errors.New("smtp down")
	if err := e.svc.SendVerificationEmail(t.Context(), res.User.ID); err == nil {
		t.Fatal("mail failure swallowed on an explicit request")
	}
}

func oauthProfile() auth.OAuthProfile {
	return auth.OAuthProfile{
		Provider: domain.ProviderGitHub, AccountID: "4242", Email: "Octo@Example.com", EmailVerified: true,
		Name: "Octo", Image: "https://avatars.example/4242", AccessToken: "gho_1", Scope: "read:user",
	}
}

func TestOAuthSignIn(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()

	// 1. Unknown user: created, verified, with a github account and no password.
	res, err := e.svc.SignInWithOAuth(ctx, oauthProfile(), meta)
	if err != nil {
		t.Fatal(err)
	}
	if res.User.Email != "octo@example.com" || !res.User.EmailVerified || res.User.Image == nil || res.Token == "" {
		t.Fatalf("new oauth user = %+v", res.User)
	}
	acc, err := e.store.GetAccount(ctx, domain.ProviderGitHub, "4242")
	if err != nil || acc.UserID != res.User.ID || acc.Password != nil || *acc.AccessToken != "gho_1" {
		t.Fatalf("github account = %+v, %v", acc, err)
	}
	// ...and cannot sign in with a password.
	if _, err := e.svc.SignIn(ctx, "octo@example.com", "anything goes", meta); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("password login for oauth-only user: %v", err)
	}

	// 2. Same GitHub account again: same user, new session, refreshed token, no duplicates.
	p := oauthProfile()
	p.AccessToken = "gho_2"
	again, err := e.svc.SignInWithOAuth(ctx, p, meta)
	if err != nil || again.User.ID != res.User.ID || again.Session.ID == res.Session.ID {
		t.Fatalf("second oauth sign-in = %+v, %v", again, err)
	}
	if acc, _ = e.store.GetAccount(ctx, domain.ProviderGitHub, "4242"); *acc.AccessToken != "gho_2" {
		t.Fatalf("token not refreshed: %v", *acc.AccessToken)
	}
	if len(e.store.users) != 1 || len(e.store.accounts) != 1 {
		t.Fatalf("duplicates: %d users, %d accounts", len(e.store.users), len(e.store.accounts))
	}
}

func TestOAuthLinksVerifiedUserOnly(t *testing.T) {
	t.Parallel()
	ctx := t.Context()

	// A local user with a VERIFIED email gets the GitHub account linked.
	e := newSessionEnv(t)
	local := e.signUp(t, "octo@example.com")
	if err := e.svc.VerifyEmail(ctx, e.lastToken(t)); err != nil {
		t.Fatal(err)
	}
	res, err := e.svc.SignInWithOAuth(ctx, oauthProfile(), meta)
	if err != nil || res.User.ID != local.User.ID {
		t.Fatalf("link = %+v, %v", res.User, err)
	}
	if _, err := e.store.GetCredentialAccount(ctx, local.User.ID); err != nil {
		t.Fatal("linking removed the password account")
	}
	if len(e.store.users) != 1 || len(e.store.accounts) != 2 {
		t.Fatalf("%d users, %d accounts; want 1 and 2", len(e.store.users), len(e.store.accounts))
	}

	// An UNVERIFIED local user is NOT merged (account pre-hijacking defence).
	e2 := newSessionEnv(t)
	e2.signUp(t, "octo@example.com")
	if _, err := e2.svc.SignInWithOAuth(ctx, oauthProfile(), meta); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unverified local user: got %v, want ErrConflict", err)
	}
	if _, err := e2.store.GetAccount(ctx, domain.ProviderGitHub, "4242"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatal("github account was linked to an unverified local account")
	}

	// An unverified provider email is never trusted.
	p := oauthProfile()
	p.EmailVerified = false
	if _, err := newSessionEnv(t).svc.SignInWithOAuth(ctx, p, meta); err == nil {
		t.Fatal("unverified provider email accepted")
	}
}

func TestCleanupExpired(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	ctx := t.Context()
	e.signUp(t, "ada@example.com") // 1 session + 1 verification token
	if s, v, err := e.svc.CleanupExpired(ctx); err != nil || s+v != 0 {
		t.Fatalf("nothing is expired yet: %d %d %v", s, v, err)
	}
	e.clock.Advance(8 * 24 * time.Hour)
	s, v, err := e.svc.CleanupExpired(ctx)
	if err != nil || s != 1 || v != 1 {
		t.Fatalf("cleanup = %d sessions, %d verifications, %v; want 1, 1", s, v, err)
	}
}

func TestSessionConfigValidation(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	authSvc, _ := newAuthSvc(t, store)
	_, err := NewSessionAuthService(SessionAuthDeps{Auth: authSvc, Config: SessionConfig{TTL: time.Hour, UpdateAge: 2 * time.Hour}})
	if err == nil {
		t.Fatal("update age longer than ttl accepted")
	}
}

// The periodic cleanup goroutine deletes expired rows and stops when its context is cancelled.
func TestRunCleanupStopsWithContext(t *testing.T) {
	t.Parallel()
	e := newSessionEnv(t)
	e.signUp(t, "ada@example.com")
	e.clock.Advance(8 * 24 * time.Hour) // the session and the verification token are now expired

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		e.svc.RunCleanup(ctx, 5*time.Millisecond)
		close(done)
	}()

	deadline := time.After(5 * time.Second)
	for {
		e.store.mu.Lock()
		left := len(e.store.sessions) + len(e.store.verifs)
		e.store.mu.Unlock()
		if left == 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("expired rows were not cleaned up (%d left)", left)
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("RunCleanup did not return after cancel (goroutine leak)")
	}
}
