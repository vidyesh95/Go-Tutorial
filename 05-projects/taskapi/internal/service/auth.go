package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// PasswordHasher is satisfied by *auth.PasswordHasher.
type PasswordHasher interface {
	Hash(password string) (string, error)
	Compare(hash, password string) error
}

// TokenIssuer is satisfied by *auth.Issuer.
type TokenIssuer interface {
	Issue(userID string) (token string, expiresAt time.Time, err error)
}

// LoginResult is what a successful login returns.
type LoginResult struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// AuthService handles signup, login and profile lookup.
type AuthService struct {
	users  UserRepo
	hasher PasswordHasher
	tokens TokenIssuer
	now    clock
	newID  idGen

	// dummyHash is a valid hash of a throwaway password. Login compares against it when the
	// email is unknown so that "no such user" costs the same ~250ms as "wrong password";
	// otherwise response time reveals which emails are registered.
	dummyHash string
}

// NewAuthService wires the service. It hashes once at construction to prepare dummyHash.
func NewAuthService(users UserRepo, hasher PasswordHasher, tokens TokenIssuer) (*AuthService, error) {
	dummy, err := hasher.Hash("taskapi-timing-equalizer")
	if err != nil {
		return nil, fmt.Errorf("service: prepare dummy hash: %w", err)
	}
	return &AuthService{users: users, hasher: hasher, tokens: tokens, now: defaultClock, newID: newID, dummyHash: dummy}, nil
}

// Signup validates the input, hashes the password and stores the new user. A duplicate email
// surfaces as domain.ErrConflict.
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
	u := domain.User{
		ID: s.newID(), Email: in.Email, Name: in.Name, PasswordHash: hash, CreatedAt: s.now(),
	}
	if err := s.users.CreateUser(ctx, u); err != nil {
		return domain.User{}, fmt.Errorf("signup: %w", err)
	}
	return u, nil
}

// Login verifies credentials and issues a token. Unknown email and wrong password produce the
// SAME error (domain.ErrUnauthorized) and take about the same time, so a caller learns nothing
// about which accounts exist.
func (s *AuthService) Login(ctx context.Context, email, password string) (LoginResult, error) {
	email = normalizeEmail(email)
	// Cap the input before the expensive hash: bcrypt only reads 72 bytes anyway, and a
	// multi-megabyte "password" would be a cheap way to burn CPU.
	if len(password) > domain.MaxPasswordBytes {
		return LoginResult{}, fmt.Errorf("login: %w", domain.ErrUnauthorized)
	}

	u, err := s.users.GetUserByEmail(ctx, email)
	switch {
	case errors.Is(err, domain.ErrNotFound):
		_ = s.hasher.Compare(s.dummyHash, password) // burn the same time; result ignored
		return LoginResult{}, fmt.Errorf("login: unknown email: %w", domain.ErrUnauthorized)
	case err != nil:
		return LoginResult{}, fmt.Errorf("login: %w", err)
	}

	if err := s.hasher.Compare(u.PasswordHash, password); err != nil {
		if errors.Is(err, auth.ErrPasswordMismatch) {
			return LoginResult{}, fmt.Errorf("login: bad password: %w", domain.ErrUnauthorized)
		}
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
