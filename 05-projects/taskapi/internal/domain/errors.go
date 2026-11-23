// Package domain holds the application's core types and rules: entities, input validation and
// the error vocabulary. It imports nothing from the rest of the project (no HTTP, no SQL), so
// every other layer can depend on it without creating import cycles.
package domain

import (
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// Sentinel errors. Lower layers wrap them with context (fmt.Errorf("get task: %w", err)); the
// HTTP layer finds them with errors.Is in ONE place (httpapi.mapError) and picks a status code.
// Nothing below the HTTP layer knows what a status code is.
var (
	// ErrNotFound means the entity does not exist OR the caller may not see it. Returning the
	// same error for both avoids telling an attacker which ids belong to other users.
	ErrNotFound = errors.New("not found")
	// ErrConflict means a uniqueness rule was violated (duplicate email, duplicate project name).
	ErrConflict = errors.New("conflict")
	// ErrUnauthorized means the caller could not be authenticated (bad credentials or token).
	ErrUnauthorized = errors.New("unauthorized")
	// ErrForbidden means the caller is authenticated but not allowed to do this. Resource
	// ownership checks deliberately use ErrNotFound instead; this one is for future role checks
	// (e.g. admin-only endpoints) where revealing existence is not a concern.
	ErrForbidden = errors.New("forbidden")
	// ErrInvalidToken means a single-use emailed token (email verification, password reset) is
	// unknown, already used or expired. One error for all three: telling them apart would help
	// someone guessing tokens.
	ErrInvalidToken = errors.New("invalid or expired token")
)

// ValidationError reports which input fields were rejected and why. It is a pointer type so
// callers use errors.As with *ValidationError.
type ValidationError struct {
	Fields map[string]string // field name -> human-readable problem
}

// Error lists the fields in sorted order so messages (and tests) are deterministic.
func (e *ValidationError) Error() string {
	parts := make([]string, 0, len(e.Fields))
	for _, name := range slices.Sorted(maps.Keys(e.Fields)) {
		parts = append(parts, fmt.Sprintf("%s: %s", name, e.Fields[name]))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}

// fieldErrors accumulates problems so a client sees every mistake in one response instead of
// fixing them one round-trip at a time.
type fieldErrors map[string]string

func (f fieldErrors) add(field, msg string) {
	// Keep the first message per field; later checks on the same field are usually consequences.
	if _, exists := f[field]; !exists {
		f[field] = msg
	}
}

// err returns nil when nothing was recorded. Returning a typed nil *ValidationError as an
// error would be non-nil (the classic nil-interface gotcha), hence the explicit check.
func (f fieldErrors) err() error {
	if len(f) == 0 {
		return nil
	}
	return &ValidationError{Fields: f}
}

// NewValidationError builds a single-field *ValidationError. Other layers (cursor parsing, query
// parameters) use it so every "bad input" ends up as the same error type.
func NewValidationError(field, msg string) error {
	return &ValidationError{Fields: map[string]string{field: msg}}
}
