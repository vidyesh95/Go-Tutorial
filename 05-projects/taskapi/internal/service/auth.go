package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// PasswordHasher is satisfied by *auth.PasswordHasher.
type PasswordHasher interface {
	Hash(password string) (string, error)
	// Verify reports whether password matches the stored hash and whether that hash should be
	// replaced because it was made with outdated parameters. err means the STORED hash is unusable.
	Verify(password, encoded string) (ok, needsRehash bool, err error)
}

// TokenIssuer is satisfied by *auth.Issuer.
type TokenIssuer interface {
	Issue(userID string) (token string, expiresAt time.Time, err error)
}

// LoginResult is what a successful JWT login returns.
type LoginResult struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuthService handles signup, credential checking and JWT login. Cookie sessions build on the
// same two primitives (Signup and Authenticate) in SessionAuthService, so BOTH authentication
// modes share one definition of "a valid email and password".
type AuthService struct {
	users    UserRepo
	accounts AccountRepo
	hasher   PasswordHasher
	tokens   TokenIssuer
	log      *slog.Logger
	now      clock
	newID    idGen

	// dummyHash is a valid hash of a throwaway password, made with the CURRENT parameters.
	// Authenticate verifies against it when the email is unknown so that "no such user" costs the
	// same ~50 ms of Argon2id work as "wrong password"; otherwise response time reveals which
	// emails are registered.
	dummyHash string
}

// NewAuthService wires the service. It hashes once at construction to prepare dummyHash. log may
// be nil.
func NewAuthService(users UserRepo, accounts AccountRepo, hasher PasswordHasher, tokens TokenIssuer, log *slog.Logger) (*AuthService, error) {
	dummy, err := hasher.Hash("taskapi-timing-equalizer")
	if err != nil {
		return nil, fmt.Errorf("service: prepare dummy hash: %w", err)
	}
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	return &AuthService{
		users: users, accounts: accounts, hasher: hasher, tokens: tokens, log: log,
		now: defaultClock, newID: newID, dummyHash: dummy,
	}, nil
}

// Signup validates the input, hashes the password and stores the new user together with its
// credential account in ONE transaction. A duplicate email surfaces as domain.ErrConflict.
//
// Tradeoff: reporting "email already registered" lets anyone probe which emails have accounts
// (user enumeration). Most products accept that for signup UX; high-security ones instead
// always answer "check your inbox" and send the real outcome by email.
func (s *AuthService) Signup(ctx context.Context, in domain.SignupInput) (domain.User, error) {
	in.Normalize()
	if err := in.Validate(); err != nil {
		return domain.User{}, err
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return domain.User{}, fmt.Errorf("signup: %w", err)
	}
	now := s.now()
	u := domain.User{ID: s.newID(), Email: in.Email, Name: in.Name, CreatedAt: now, UpdatedAt: now}
	acc := domain.Account{
		ID: s.newID(), UserID: u.ID, ProviderID: domain.ProviderCredential,
		AccountID: u.ID, // for credential accounts the "provider's id for the user" is our own
		Password:  &hash, CreatedAt: now, UpdatedAt: now,
	}
	if err := s.users.CreateUserWithAccount(ctx, u, acc); err != nil {
		return domain.User{}, fmt.Errorf("signup: %w", err)
	}
	return u, nil
}

// Authenticate checks an email and password and returns the user. Unknown email, no password on
// the account (OAuth-only user), unusable legacy hash and wrong password all produce the SAME
// error (domain.ErrUnauthorized) and take about the same time, so a caller learns nothing about
// which accounts exist.
//
// If the password is right but the stored hash uses outdated Argon2 parameters, the hash is
// replaced with a fresh one ("rehash on login"). Login is the only moment we hold the plaintext,
// so this is how raising the cost parameters upgrades every active account without a reset.
func (s *AuthService) Authenticate(ctx context.Context, email, password string) (domain.User, error) {
	email = normalizeEmail(email)
	// Cap the input before the expensive hash: a multi-megabyte "password" would be a cheap way
	// to burn CPU and memory.
	if len(password) > domain.MaxPasswordBytes {
		return domain.User{}, fmt.Errorf("authenticate: %w", domain.ErrUnauthorized)
	}
	fail := func(reason string) (domain.User, error) {
		// Burn the same time as a real verification; the result is irrelevant.
		_, _, _ = s.hasher.Verify(password, s.dummyHash)
		return domain.User{}, fmt.Errorf("authenticate: %s: %w", reason, domain.ErrUnauthorized)
	}

	u, err := s.users.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return fail("unknown email")
	case err != nil:
		return domain.User{}, fmt.Errorf("authenticate: %w", err)
	}
	acc, err := s.accounts.GetCredentialAccount(ctx, u.ID)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		return fail("no credential account") // e.g. signed up with GitHub only
	case err != nil:
		return domain.User{}, fmt.Errorf("authenticate: %w", err)
	case acc.Password == nil:
		return fail("credential account without password")
	}

	ok, needsRehash, err := s.hasher.Verify(password, *acc.Password)
	switch {
	case errors.Is(err, auth.ErrUnsupportedAlgorithm):
		// A hash from before the Argon2id switch (bcrypt). We refuse to verify it; the user must
		// use "forget password". See the README, "Migrating legacy hashes".
		s.log.WarnContext(ctx, "login with a legacy password hash; user must reset their password", "user_id", u.ID)
		return fail("legacy hash")
	case err != nil:
		return domain.User{}, fmt.Errorf("authenticate: %w", err) // corrupt hash: a real server fault
	case !ok:
		return domain.User{}, fmt.Errorf("authenticate: bad password: %w", domain.ErrUnauthorized)
	}

	if needsRehash {
		// Best effort: failing to upgrade the hash must not fail a login that was correct.
		if newHash, err := s.hasher.Hash(password); err != nil {
			s.log.WarnContext(ctx, "rehash on login failed", "user_id", u.ID, "error", err)
		} else if err := s.accounts.UpdatePasswordHash(ctx, acc.ID, newHash, s.now()); err != nil {
			s.log.WarnContext(ctx, "storing rehashed password failed", "user_id", u.ID, "error", err)
		}
	}
	return u, nil
}

// Login verifies credentials and issues a JWT (the stateless, Bearer mode of authentication).
func (s *AuthService) Login(ctx context.Context, email, password string) (LoginResult, error) {
	u, err := s.Authenticate(ctx, email, password)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	token, exp, err := s.tokens.Issue(u.ID)
	if err != nil {
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}
	return LoginResult{Token: token, ExpiresAt: exp}, nil
}

// GetUser returns the profile of the authenticated user.
func (s *AuthService) GetUser(ctx context.Context, id string) (domain.User, error) {
	u, err := s.users.GetUserByID(ctx, id)
	if err != nil {
		return domain.User{}, fmt.Errorf("get user: %w", err)
	}
	return u, nil
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }
