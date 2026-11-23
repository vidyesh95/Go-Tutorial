package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// Lifetimes of the emailed single-use tokens.
const (
	emailVerificationTTL = 24 * time.Hour
	passwordResetTTL     = time.Hour
	maxUserAgentBytes    = 512 // user agents are client-controlled text that we store and echo
)

// SessionConfig tunes cookie sessions.
type SessionConfig struct {
	// TTL is how long a session lives after it was created or last refreshed (default 7 days).
	TTL time.Duration
	// UpdateAge is Better Auth's "updateAge": a session is only extended when it has not been
	// touched for at least this long (default 24h), so an active user's expiry slides forward
	// without a database WRITE on every request.
	UpdateAge time.Duration
	// PublicURL is the externally visible base URL, used to build links in emails.
	PublicURL string
	// Now overrides the clock (tests). nil means time.Now.
	Now func() time.Time
}

// ClientMeta is what we remember about the device a session was created on, so users can
// recognise and revoke their sessions ("Chrome on macOS, 203.0.113.9").
type ClientMeta struct {
	IP        string
	UserAgent string
}

// SessionResult is a freshly created session. Token is the raw cookie value: it exists only
// here, in memory, on its way to the Set-Cookie header. The database has only its hash.
type SessionResult struct {
	User    domain.User
	Session domain.Session
	Token   string
}

// SessionState is the outcome of authenticating a cookie.
type SessionState struct {
	Session domain.Session
	// Refreshed is true when expiry was just extended; the HTTP layer then re-sends the cookie
	// so the browser's copy lives as long as the database row.
	Refreshed bool
}

// SessionAuthDeps are the collaborators of SessionAuthService.
type SessionAuthDeps struct {
	Auth          *AuthService
	Users         UserRepo
	Accounts      AccountRepo
	Sessions      SessionRepo
	Verifications VerificationRepo
	Hasher        PasswordHasher
	Mailer        Mailer
	Log           *slog.Logger // nil = discard
	Config        SessionConfig
}

// SessionAuthService implements the Better Auth-style, database-backed authentication mode:
// cookie sessions, email verification, password reset and OAuth sign-in.
//
// A session is a random token the browser keeps in an HttpOnly cookie. The database keeps its
// SHA-256 plus metadata. Because the server holds the state, it can do what a stateless JWT
// cannot: revoke one session, list devices, sign out everywhere after a password change.
// The price is one database lookup per authenticated request.
type SessionAuthService struct {
	SessionAuthDeps
	now   clock
	newID idGen
}

// NewSessionAuthService validates the configuration and applies defaults.
func NewSessionAuthService(d SessionAuthDeps) (*SessionAuthService, error) {
	if d.Config.TTL == 0 {
		d.Config.TTL = 7 * 24 * time.Hour
	}
	if d.Config.UpdateAge == 0 {
		d.Config.UpdateAge = 24 * time.Hour
	}
	if d.Config.TTL < 0 || d.Config.UpdateAge < 0 || d.Config.UpdateAge > d.Config.TTL {
		return nil, errors.New("service: session update age must be between 0 and the session ttl")
	}
	d.Config.PublicURL = strings.TrimRight(d.Config.PublicURL, "/")
	if d.Log == nil {
		d.Log = slog.New(slog.DiscardHandler)
	}
	s := &SessionAuthService{SessionAuthDeps: d, now: defaultClock, newID: newID}
	if d.Config.Now != nil {
		s.now = func() time.Time { return d.Config.Now().UTC().Truncate(time.Millisecond) }
	}
	return s, nil
}

// TTL returns the session lifetime (the HTTP layer uses it for the cookie's Max-Age).
func (s *SessionAuthService) TTL() time.Duration { return s.Config.TTL }

// --- Sessions -----------------------------------------------------------------------------

// SignUp creates the account (user + credential account, atomically), starts a session and sends
// the verification email. Like Better Auth with email verification not enforced, the user is
// signed in immediately; unverified users can use the API (see README, next steps, for
// requiring verification).
func (s *SessionAuthService) SignUp(ctx context.Context, in domain.SignupInput, meta ClientMeta) (SessionResult, error) {
	u, err := s.Auth.Signup(ctx, in)
	if err != nil {
		return SessionResult{}, err
	}
	res, err := s.startSession(ctx, u, meta)
	if err != nil {
		return SessionResult{}, err
	}
	// The account exists and the user is signed in; a mail outage must not undo that. They can
	// request another email later.
	if err := s.sendVerification(ctx, u); err != nil {
		s.Log.WarnContext(ctx, "sending verification email failed", "user_id", u.ID, "error", err)
	}
	return res, nil
}

// SignIn checks the password (same code path, same timing and same generic error as the JWT
// login) and starts a session.
func (s *SessionAuthService) SignIn(ctx context.Context, email, password string, meta ClientMeta) (SessionResult, error) {
	u, err := s.Auth.Authenticate(ctx, email, password)
	if err != nil {
		return SessionResult{}, fmt.Errorf("sign in: %w", err)
	}
	return s.startSession(ctx, u, meta)
}

func (s *SessionAuthService) startSession(ctx context.Context, u domain.User, meta ClientMeta) (SessionResult, error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return SessionResult{}, err
	}
	now := s.now()
	sess := domain.Session{
		ID: s.newID(), UserID: u.ID, TokenHash: hash, ExpiresAt: now.Add(s.Config.TTL),
		IPAddress: meta.IP, UserAgent: truncate(meta.UserAgent, maxUserAgentBytes),
		CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Sessions.CreateSession(ctx, sess); err != nil {
		return SessionResult{}, fmt.Errorf("start session: %w", err)
	}
	return SessionResult{User: u, Session: sess, Token: token}, nil
}

// Authenticate resolves a cookie token to a live session. Unknown and expired tokens give the
// same domain.ErrUnauthorized. An expired row is deleted on the spot ("lazy expiry"); the
// periodic cleanup catches the ones nobody ever presents again.
//
// Sliding expiry: if the session has not been touched for UpdateAge, its expiry is pushed to
// now+TTL. Active users never get logged out; abandoned sessions still end TTL after last use.
func (s *SessionAuthService) Authenticate(ctx context.Context, token string) (SessionState, error) {
	if token == "" {
		return SessionState{}, domain.ErrUnauthorized
	}
	sess, err := s.Sessions.GetSessionByTokenHash(ctx, auth.HashToken(token))
	if errors.Is(err, domain.ErrNotFound) {
		return SessionState{}, domain.ErrUnauthorized
	} else if err != nil {
		return SessionState{}, fmt.Errorf("authenticate session: %w", err)
	}

	now := s.now()
	if !now.Before(sess.ExpiresAt) {
		if err := s.Sessions.DeleteSessionByID(ctx, sess.ID); err != nil {
			s.Log.WarnContext(ctx, "deleting expired session failed", "session_id", sess.ID, "error", err)
		}
		return SessionState{}, domain.ErrUnauthorized
	}

	state := SessionState{Session: sess}
	if now.Sub(sess.UpdatedAt) >= s.Config.UpdateAge {
		exp := now.Add(s.Config.TTL)
		if err := s.Sessions.ExtendSession(ctx, sess.ID, exp, now); errors.Is(err, domain.ErrNotFound) {
			return SessionState{}, domain.ErrUnauthorized // revoked between the lookup and now
		} else if err != nil {
			return SessionState{}, fmt.Errorf("extend session: %w", err)
		}
		state.Session.ExpiresAt, state.Session.UpdatedAt, state.Refreshed = exp, now, true
	}
	return state, nil
}

// SignOut deletes the session behind token. It is idempotent: an unknown or already-deleted
// token is not an error, so a client with a stale cookie can always sign out cleanly.
func (s *SessionAuthService) SignOut(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}
	if err := s.Sessions.DeleteSessionByTokenHash(ctx, auth.HashToken(token)); err != nil {
		return fmt.Errorf("sign out: %w", err)
	}
	return nil
}

// ListSessions returns the user's active sessions, newest first.
func (s *SessionAuthService) ListSessions(ctx context.Context, userID string) ([]domain.Session, error) {
	list, err := s.Sessions.ListActiveSessions(ctx, userID, s.now())
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return list, nil
}

// RevokeSession ends one of the user's own sessions. Someone else's session id is
// domain.ErrNotFound, exactly like a nonexistent one.
func (s *SessionAuthService) RevokeSession(ctx context.Context, userID, sessionID string) error {
	if err := s.Sessions.DeleteUserSession(ctx, userID, sessionID); err != nil {
		return fmt.Errorf("revoke session: %w", err)
	}
	return nil
}

// RevokeOtherSessions signs the user out everywhere except the current session ("log out other
// devices").
func (s *SessionAuthService) RevokeOtherSessions(ctx context.Context, userID, currentSessionID string) error {
	if err := s.Sessions.DeleteOtherSessions(ctx, userID, currentSessionID); err != nil {
		return fmt.Errorf("revoke other sessions: %w", err)
	}
	return nil
}

// --- Email verification --------------------------------------------------------------------

// SendVerificationEmail emails the user a link to confirm their address. A user whose email is
// already verified gets nothing (idempotent). Asking again invalidates the earlier link.
func (s *SessionAuthService) SendVerificationEmail(ctx context.Context, userID string) error {
	u, err := s.Users.GetUserByID(ctx, userID)
	if err != nil {
		return fmt.Errorf("send verification email: %w", err)
	}
	if u.EmailVerified {
		return nil
	}
	return s.sendVerification(ctx, u)
}

func (s *SessionAuthService) sendVerification(ctx context.Context, u domain.User) error {
	token, err := s.issueVerification(ctx, domain.PurposeEmailVerification, u.ID, emailVerificationTTL)
	if err != nil {
		return err
	}
	link := s.Config.PublicURL + "/v1/session-auth/verify-email?token=" + url.QueryEscape(token)
	body := fmt.Sprintf("Hi %s,\n\nConfirm your email address by opening this link (valid for 24 hours):\n\n%s\n\nIf you did not create this account you can ignore this message.\n", u.Name, link)
	return s.Mailer.Send(ctx, u.Email, "Confirm your email address", body)
}

// VerifyEmail consumes a verification token and marks the user's email verified. The token is
// single-use and expires after 24 hours; every failure is domain.ErrInvalidToken.
func (s *SessionAuthService) VerifyEmail(ctx context.Context, token string) error {
	userID, err := s.consume(ctx, domain.PurposeEmailVerification, token)
	if err != nil {
		return err
	}
	if err := s.Users.MarkEmailVerified(ctx, userID, s.now()); errors.Is(err, domain.ErrNotFound) {
		return domain.ErrInvalidToken // the user was deleted after the email went out
	} else if err != nil {
		return fmt.Errorf("verify email: %w", err)
	}
	return nil
}

// --- Password reset -------------------------------------------------------------------------

// ForgetPassword emails a reset link if an account with that email exists. It returns nil either
// way: answering "no such user" would let anyone enumerate registered emails. (A remaining side
// channel is response TIME, since only real accounts trigger the database write and the email.
// Production systems hand the mail to a background queue so both paths take equally long.)
func (s *SessionAuthService) ForgetPassword(ctx context.Context, email string) error {
	email = normalizeEmail(email)
	if err := domain.ValidateEmail(email); err != nil {
		return err // malformed input is a plain 422; it reveals nothing about accounts
	}
	u, err := s.Users.GetUserByEmail(ctx, email)
	if errors.Is(err, domain.ErrNotFound) {
		return nil
	} else if err != nil {
		return fmt.Errorf("forget password: %w", err)
	}
	token, err := s.issueVerification(ctx, domain.PurposePasswordReset, u.ID, passwordResetTTL)
	if err != nil {
		return err
	}
	// The link points at the FRONTEND page that collects the new password and then POSTs
	// {token, new_password} to /v1/session-auth/reset-password.
	link := s.Config.PublicURL + "/reset-password?token=" + url.QueryEscape(token)
	body := fmt.Sprintf("Hi %s,\n\nSomeone asked to reset the password of this account. Use this link within 1 hour:\n\n%s\n\nIf it was not you, ignore this message: your password has not changed.\n", u.Name, link)
	if err := s.Mailer.Send(ctx, u.Email, "Reset your password", body); err != nil {
		return fmt.Errorf("forget password: send mail: %w", err)
	}
	return nil
}

// ResetPassword completes a reset: it consumes the single-use token, stores the new Argon2id
// hash and deletes ALL of the user's sessions in one transaction, so anybody who was logged in
// with the old password (or a stolen cookie) is out.
//
// The new password is validated BEFORE the token is consumed, so a too-short password does not
// burn the user's link.
func (s *SessionAuthService) ResetPassword(ctx context.Context, token, newPassword string) error {
	if err := domain.ValidatePassword("new_password", newPassword); err != nil {
		return err
	}
	userID, err := s.consume(ctx, domain.PurposePasswordReset, token)
	if err != nil {
		return err
	}
	hash, err := s.Hasher.Hash(newPassword)
	if err != nil {
		return fmt.Errorf("reset password: %w", err)
	}
	now := s.now()
	acc := domain.Account{
		ID: s.newID(), UserID: userID, ProviderID: domain.ProviderCredential, AccountID: userID,
		Password: &hash, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.Accounts.ResetCredentialPassword(ctx, acc); err != nil {
		if errors.Is(err, domain.ErrNotFound) { // FK violation: user deleted meanwhile
			return domain.ErrInvalidToken
		}
		return fmt.Errorf("reset password: %w", err)
	}
	return nil
}

// --- OAuth ----------------------------------------------------------------------------------

// SignInWithOAuth signs in (or up) a user authenticated by an OAuth provider and starts a
// session.
//
//  1. The provider account is already linked: sign in as its user (and refresh stored tokens).
//  2. A user with the same email exists: link the provider account to them. ONLY if the
//     provider vouches for the email (checked here) AND that user's email is verified here too.
//     Otherwise an attacker could pre-register victim@example.com with their own password and
//     wait for the victim to "Sign in with GitHub": the accounts would merge and the attacker
//     would share the victim's data ("account pre-hijacking"). Unverified local accounts get
//     domain.ErrConflict instead.
//  3. Otherwise create a new user (email verified, since the provider verified it) with the
//     provider account, atomically.
func (s *SessionAuthService) SignInWithOAuth(ctx context.Context, p auth.OAuthProfile, meta ClientMeta) (SessionResult, error) {
	email := normalizeEmail(p.Email)
	if !p.EmailVerified || email == "" {
		return SessionResult{}, domain.NewValidationError("email", "the provider account has no verified primary email address")
	}
	now := s.now()

	acc, err := s.Accounts.GetAccount(ctx, p.Provider, p.AccountID)
	switch {
	case err == nil:
		applyOAuthTokens(&acc, p, now)
		if err := s.Accounts.UpdateAccountTokens(ctx, acc); err != nil {
			return SessionResult{}, fmt.Errorf("oauth sign in: %w", err)
		}
		u, err := s.Users.GetUserByID(ctx, acc.UserID)
		if err != nil {
			return SessionResult{}, fmt.Errorf("oauth sign in: %w", err)
		}
		return s.startSession(ctx, u, meta)
	case !errors.Is(err, domain.ErrNotFound):
		return SessionResult{}, fmt.Errorf("oauth sign in: %w", err)
	}

	acc = domain.Account{
		ID: s.newID(), ProviderID: p.Provider, AccountID: p.AccountID, CreatedAt: now,
	}
	applyOAuthTokens(&acc, p, now)

	u, err := s.Users.GetUserByEmail(ctx, email)
	switch {
	case err == nil:
		if !u.EmailVerified {
			return SessionResult{}, fmt.Errorf("oauth sign in: existing account has an unverified email: %w", domain.ErrConflict)
		}
		acc.UserID = u.ID
		if err := s.Accounts.CreateAccount(ctx, acc); err != nil {
			return SessionResult{}, fmt.Errorf("oauth sign in: link account: %w", err)
		}
	case errors.Is(err, domain.ErrNotFound):
		u = domain.User{ID: s.newID(), Email: email, Name: p.Name, EmailVerified: true, CreatedAt: now, UpdatedAt: now}
		if p.Image != "" {
			u.Image = &p.Image
		}
		acc.UserID = u.ID
		if err := s.Users.CreateUserWithAccount(ctx, u, acc); err != nil {
			return SessionResult{}, fmt.Errorf("oauth sign in: create user: %w", err)
		}
	default:
		return SessionResult{}, fmt.Errorf("oauth sign in: %w", err)
	}
	return s.startSession(ctx, u, meta)
}

func applyOAuthTokens(a *domain.Account, p auth.OAuthProfile, now time.Time) {
	a.AccessToken, a.RefreshToken, a.IDToken = nonEmpty(p.AccessToken), nonEmpty(p.RefreshToken), nonEmpty(p.IDToken)
	a.AccessTokenExpiresAt, a.RefreshTokenExpiresAt = p.AccessTokenExpiresAt, p.RefreshTokenExpiresAt
	a.Scope = nonEmpty(p.Scope)
	a.UpdatedAt = now
}

func nonEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// --- Cleanup ----------------------------------------------------------------------------------

// CleanupExpired deletes expired sessions and verification tokens and reports how many.
// Expired rows are never honoured (lookups check expiry), so this is housekeeping, not security.
func (s *SessionAuthService) CleanupExpired(ctx context.Context) (sessions, verifications int64, err error) {
	now := s.now()
	if sessions, err = s.Sessions.DeleteExpiredSessions(ctx, now); err != nil {
		return 0, 0, err
	}
	if verifications, err = s.Verifications.DeleteExpiredVerifications(ctx, now); err != nil {
		return sessions, 0, err
	}
	return sessions, verifications, nil
}

// RunCleanup calls CleanupExpired every interval until ctx is cancelled, then returns. Start it
// with `go`; it is the only background goroutine of this service.
func (s *SessionAuthService) RunCleanup(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			sessions, verifs, err := s.CleanupExpired(ctx)
			switch {
			case err != nil && ctx.Err() == nil:
				s.Log.ErrorContext(ctx, "expired-row cleanup failed", "error", err)
			case sessions+verifs > 0:
				s.Log.InfoContext(ctx, "expired rows cleaned up", "sessions", sessions, "verifications", verifs)
			}
		}
	}
}

// --- helpers ----------------------------------------------------------------------------------

// issueVerification creates a token for "<purpose>:<userID>", stores its hash (replacing older
// tokens for the same purpose and user) and returns the raw token for the email.
func (s *SessionAuthService) issueVerification(ctx context.Context, purpose, userID string, ttl time.Duration) (string, error) {
	token, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	now := s.now()
	err = s.Verifications.ReplaceVerification(ctx, domain.Verification{
		ID: s.newID(), Identifier: purpose + ":" + userID, Value: hash,
		ExpiresAt: now.Add(ttl), CreatedAt: now, UpdatedAt: now,
	})
	if err != nil {
		return "", fmt.Errorf("store %s token: %w", purpose, err)
	}
	return token, nil
}

// consume burns a token and returns the user id it was issued for.
func (s *SessionAuthService) consume(ctx context.Context, purpose, token string) (userID string, err error) {
	if token == "" {
		return "", domain.ErrInvalidToken
	}
	identifier, err := s.Verifications.ConsumeVerification(ctx, purpose, auth.HashToken(token), s.now())
	if errors.Is(err, domain.ErrNotFound) {
		return "", domain.ErrInvalidToken
	} else if err != nil {
		return "", fmt.Errorf("consume %s token: %w", purpose, err)
	}
	userID, ok := strings.CutPrefix(identifier, purpose+":")
	if !ok || userID == "" {
		return "", fmt.Errorf("consume %s token: malformed identifier %q", purpose, identifier)
	}
	return userID, nil
}

// truncate cuts s to at most n bytes without splitting a UTF-8 character.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return strings.ToValidUTF8(s[:n], "")
}
