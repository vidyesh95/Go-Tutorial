// Package domain holds the core types and errors that every layer speaks.
//
// It imports nothing from this module, so handler, service and store can all depend on
// it without creating import cycles. Keep it small: plain data and sentinel errors, no
// logic that talks to the outside world, and NO json tags (the HTTP shape is the handler's
// business; the storage shape is the store's).
package domain

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"uuid"
)

type User struct {
	ID        uuid.UUID
	Email     string
	Name      string
	CreatedAt time.Time
}

// Sentinel errors: the business vocabulary. The service and store return these (wrapped
// with context via %w if they like); the handler maps them to HTTP status codes in one
// place. Nothing below the handler knows what a status code is.
var (
	ErrNotFound   = errors.New("not found")
	ErrEmailTaken = errors.New("email already registered")
)

// ValidationError carries per-field messages. A custom type (rather than a sentinel)
// because the caller needs the data, found with errors.As / errors.AsType.
type ValidationError struct {
	Fields map[string]string
}

func (e *ValidationError) Error() string {
	keys := make([]string, 0, len(e.Fields))
	for k := range e.Fields {
		keys = append(keys, k)
	}
	slices.Sort(keys) // map order is random; error text should be stable
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s: %s", k, e.Fields[k]))
	}
	return "validation failed: " + strings.Join(parts, "; ")
}
