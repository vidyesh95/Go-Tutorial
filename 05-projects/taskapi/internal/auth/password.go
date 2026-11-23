// Package auth contains the two security primitives of the service: password hashing (bcrypt)
// and session tokens (signed JWTs). It knows nothing about HTTP or the database.
package auth

import (
	"errors"
	"fmt"

	"golang.org/x/crypto/bcrypt"
)

// DefaultCost is the bcrypt work factor. Each +1 doubles the time. 12 is roughly 250ms on a
// modern core: slow enough to make offline cracking of a stolen database expensive, fast enough
// for a login endpoint. Revisit every few years as hardware improves.
const DefaultCost = 12

// ErrPasswordMismatch is returned by Compare when the password does not match the hash.
var ErrPasswordMismatch = errors.New("password does not match")

// PasswordHasher hashes and verifies passwords with bcrypt. Cost is a field (not a constant) so
// tests can use bcrypt.MinCost and stay fast.
type PasswordHasher struct {
	cost int
}

// NewPasswordHasher returns a hasher using the given bcrypt cost (4-31).
func NewPasswordHasher(cost int) (*PasswordHasher, error) {
	if cost < bcrypt.MinCost || cost > bcrypt.MaxCost {
		return nil, fmt.Errorf("auth: bcrypt cost %d out of range [%d, %d]", cost, bcrypt.MinCost, bcrypt.MaxCost)
	}
	return &PasswordHasher{cost: cost}, nil
}

// Hash returns a salted bcrypt hash. bcrypt generates a random salt per call and embeds salt and
// cost in the output string, so two hashes of the same password differ and no separate salt
// column is needed. Input over 72 bytes is rejected by bcrypt itself (and by validation earlier).
func (h *PasswordHasher) Hash(password string) (string, error) {
	b, err := bcrypt.GenerateFromPassword([]byte(password), h.cost)
	if err != nil {
		return "", fmt.Errorf("auth: hash password: %w", err)
	}
	return string(b), nil
}

// Compare checks password against a stored hash. The comparison inside bcrypt is constant-time.
// A mismatch returns ErrPasswordMismatch; anything else (e.g. a corrupt hash) is a real error.
func (h *PasswordHasher) Compare(hash, password string) error {
	err := bcrypt.CompareHashAndPassword([]byte(hash), []byte(password))
	switch {
	case err == nil:
		return nil
	case errors.Is(err, bcrypt.ErrMismatchedHashAndPassword):
		return ErrPasswordMismatch
	default:
		return fmt.Errorf("auth: compare password: %w", err)
	}
}
