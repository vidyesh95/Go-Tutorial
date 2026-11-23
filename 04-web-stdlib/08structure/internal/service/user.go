// Package service holds business rules: validation, normalisation, id and time generation,
// orchestration of stores. It knows nothing about HTTP (no http.Request, no status codes)
// and nothing about SQL.
package service

import (
	"context"
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/domain"
)

// UserStore is declared HERE, in the consumer, and lists only what the service uses.
// Benefits: the service is testable with a 10-line fake; the interface cannot grow
// into a 25-method god-interface shared by everyone; the store package does not import service.
type UserStore interface {
	Create(ctx context.Context, u domain.User) error
	Get(ctx context.Context, id uuid.UUID) (domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
}

type Users struct {
	store UserStore
	now   func() time.Time // injected clock: tests can freeze time
}

func NewUsers(store UserStore) *Users {
	return &Users{store: store, now: func() time.Time { return time.Now().UTC() }}
}

type RegisterInput struct {
	Email string
	Name  string
}

// Register validates, normalises and persists a new user.
func (s *Users) Register(ctx context.Context, in RegisterInput) (domain.User, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	name := strings.TrimSpace(in.Name)

	fields := map[string]string{}
	// mail.ParseAddress also accepts "Ann <a@b.c>"; requiring the parsed address to equal the
	// input rejects display names.
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		fields["email"] = "must be a valid email address"
	}
	if n := utf8.RuneCountInString(name); n == 0 || n > 80 {
		fields["name"] = "must be 1 to 80 characters"
	}
	if len(fields) > 0 {
		return domain.User{}, &domain.ValidationError{Fields: fields}
	}

	u := domain.User{ID: uuid.NewV7(), Email: email, Name: name, CreatedAt: s.now()}
	if err := s.store.Create(ctx, u); err != nil {
		return domain.User{}, fmt.Errorf("register user: %w", err) // add context, keep the cause
	}
	return u, nil
}

func (s *Users) Get(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return s.store.Get(ctx, id)
}

func (s *Users) List(ctx context.Context) ([]domain.User, error) {
	return s.store.List(ctx)
}
