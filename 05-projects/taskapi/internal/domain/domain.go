package domain

import (
	"fmt"
	"net/mail"
	"strings"
	"time"
	"unicode/utf8"
)

// Limits shared by validation and documentation (api/openapi.yaml mirrors them).
const (
	MaxEmailLength   = 254 // RFC 5321 path limit
	MaxNameLength    = 100
	MaxTitleLength   = 200
	MinPriority      = 1
	MaxPriority      = 5
	DefaultPriority  = 3
	DefaultPageSize  = 20
	MaxPageSize      = 100
	MinPasswordBytes = 8
	// MaxPasswordBytes bounds the CPU a single login or signup can make us spend. Argon2id,
	// unlike bcrypt (72-byte limit), would happily hash megabytes; 128 bytes is far more than
	// any typed password or password-manager output. Mirrors auth.MaxPasswordBytes.
	MaxPasswordBytes = 128
)

// Account provider ids. In Better Auth's data model a user can have several "accounts", one per
// way of signing in.
const (
	ProviderCredential = "credential" // email + password; the account holds the argon2id hash
	ProviderGitHub     = "github"
)

// Purposes of a verification token. The stored identifier is "<purpose>:<user id>".
const (
	PurposeEmailVerification = "email-verification"
	PurposePasswordReset     = "password-reset"
)

// User is a person. WHO they are lives here; HOW they authenticate lives in Account rows, so the
// password hash is not part of User and no handler can leak it by accident.
type User struct {
	ID            string    `json:"id"`
	Email         string    `json:"email"`
	Name          string    `json:"name"`
	EmailVerified bool      `json:"email_verified"`
	Image         *string   `json:"image"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// Account is one way a user can sign in (Better Auth's "account" table). Never serialized.
type Account struct {
	ID         string
	UserID     string
	ProviderID string // ProviderCredential or ProviderGitHub
	// AccountID identifies the user at the provider: the user's own id for credential accounts,
	// the provider's user id (e.g. GitHub's numeric id) for OAuth accounts.
	AccountID string
	// Password is the argon2id PHC string; set only for credential accounts.
	Password *string

	// OAuth provider credentials; nil for credential accounts.
	AccessToken           *string
	RefreshToken          *string
	IDToken               *string
	AccessTokenExpiresAt  *time.Time
	RefreshTokenExpiresAt *time.Time
	Scope                 *string

	CreatedAt time.Time
	UpdatedAt time.Time
}

// Session is a server-side login session (cookie authentication). The JSON form is what clients
// may see: no user id, and above all never the token or its hash.
type Session struct {
	ID        string    `json:"id"`
	UserID    string    `json:"-"`
	TokenHash string    `json:"-"` // SHA-256 of the cookie value; see auth.NewToken
	ExpiresAt time.Time `json:"expires_at"`
	IPAddress string    `json:"ip_address"`
	UserAgent string    `json:"user_agent"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Verification is a single-use emailed token (verify email, reset password). Value holds the
// SHA-256 of the token, never the token.
type Verification struct {
	ID         string
	Identifier string // "<purpose>:<user id>"
	Value      string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
}

// Project groups tasks and belongs to exactly one user.
type Project struct {
	ID        string    `json:"id"`
	OwnerID   string    `json:"owner_id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// TaskStatus is a closed set of values; the database enforces it too with a CHECK constraint.
type TaskStatus string

const (
	StatusTodo  TaskStatus = "todo"
	StatusDoing TaskStatus = "doing"
	StatusDone  TaskStatus = "done"
)

// Valid reports whether s is one of the known statuses.
func (s TaskStatus) Valid() bool {
	switch s {
	case StatusTodo, StatusDoing, StatusDone:
		return true
	}
	return false
}

// Task is a unit of work inside a project. Ownership is derived from the project.
type Task struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Title     string     `json:"title"`
	Status    TaskStatus `json:"status"`
	Priority  int        `json:"priority"`
	// DueDate is a calendar date (YYYY-MM-DD), not an instant: "due on the 5th" has no time zone.
	DueDate   *string   `json:"due_date"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Page is one slice of a keyset-paginated list.
type Page[T any] struct {
	Items   []T
	HasMore bool
}

// NormalizeLimit applies the default and the cap to a client-supplied page size.
func NormalizeLimit(n int) int {
	switch {
	case n < 1:
		return DefaultPageSize
	case n > MaxPageSize:
		return MaxPageSize
	}
	return n
}

// --- Inputs -------------------------------------------------------------------------------

// SignupInput is the payload for creating an account.
type SignupInput struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Normalize trims and lower-cases the email so "Ada@Example.com " and "ada@example.com" are
// the same account. (Lower-casing the whole address is a practical simplification: the local
// part is technically case-sensitive, but virtually no provider treats it that way.)
func (in *SignupInput) Normalize() {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
}

// Validate checks the (normalized) input. Error messages never echo the password.
func (in SignupInput) Validate() error {
	f := fieldErrors{}
	validateEmailField(f, in.Email)
	if n := utf8.RuneCountInString(in.Name); n < 1 || n > MaxNameLength {
		f.add("name", fmt.Sprintf("must be 1-%d characters", MaxNameLength))
	}
	validatePasswordField(f, "password", in.Password)
	return f.err()
}

// isBareAddress rejects "Ada <ada@example.com>": mail.ParseAddress accepts display names, but
// we want just the address.
func isBareAddress(s string) bool {
	a, err := mail.ParseAddress(s)
	return err == nil && a.Address == s
}

// ValidatePassword applies the password length rules (bytes, so multi-byte characters count
// fully, matching what the hash function actually receives).
func ValidatePassword(field, password string) error {
	f := fieldErrors{}
	validatePasswordField(f, field, password)
	return f.err()
}

func validatePasswordField(f fieldErrors, field, password string) {
	if n := len(password); n < MinPasswordBytes || n > MaxPasswordBytes {
		f.add(field, fmt.Sprintf("must be %d-%d bytes", MinPasswordBytes, MaxPasswordBytes))
	}
}

// ValidateEmail checks that s (already normalized) is a bare, plausible email address.
func ValidateEmail(s string) error {
	f := fieldErrors{}
	validateEmailField(f, s)
	return f.err()
}

func validateEmailField(f fieldErrors, s string) {
	if _, err := mail.ParseAddress(s); err != nil || len(s) > MaxEmailLength || !isBareAddress(s) {
		f.add("email", "must be a valid email address")
	}
}

// ValidateProjectName checks a project name.
func ValidateProjectName(name string) error {
	f := fieldErrors{}
	if n := utf8.RuneCountInString(name); n < 1 || n > MaxNameLength {
		f.add("name", fmt.Sprintf("must be 1-%d characters", MaxNameLength))
	}
	return f.err()
}

// CreateTaskInput is the payload for creating a task. Pointers distinguish "not sent" (use the
// default) from an explicit zero value.
type CreateTaskInput struct {
	Title    string  `json:"title"`
	Priority *int    `json:"priority"`
	DueDate  *string `json:"due_date"`
}

// Validate checks the input.
func (in CreateTaskInput) Validate() error {
	f := fieldErrors{}
	validateTitle(f, in.Title)
	if in.Priority != nil {
		validatePriority(f, *in.Priority)
	}
	if in.DueDate != nil {
		validateDueDate(f, *in.DueDate, false)
	}
	return f.err()
}

// UpdateTaskInput is a PATCH payload: nil fields are left unchanged. To clear the due date send
// "due_date": "" (JSON null decodes to nil, which is indistinguishable from "not sent" with
// plain pointers; the empty string is the explicit "clear" signal).
type UpdateTaskInput struct {
	Title    *string     `json:"title"`
	Status   *TaskStatus `json:"status"`
	Priority *int        `json:"priority"`
	DueDate  *string     `json:"due_date"`
}

// Validate checks the provided fields and rejects an empty patch.
func (in UpdateTaskInput) Validate() error {
	f := fieldErrors{}
	if in.Title == nil && in.Status == nil && in.Priority == nil && in.DueDate == nil {
		f.add("body", "at least one of title, status, priority, due_date is required")
	}
	if in.Title != nil {
		validateTitle(f, *in.Title)
	}
	if in.Status != nil && !in.Status.Valid() {
		f.add("status", "must be one of todo, doing, done")
	}
	if in.Priority != nil {
		validatePriority(f, *in.Priority)
	}
	if in.DueDate != nil {
		validateDueDate(f, *in.DueDate, true)
	}
	return f.err()
}

// Apply copies the provided fields onto t. Call Validate first.
func (in UpdateTaskInput) Apply(t *Task, now time.Time) {
	if in.Title != nil {
		t.Title = *in.Title
	}
	if in.Status != nil {
		t.Status = *in.Status
	}
	if in.Priority != nil {
		t.Priority = *in.Priority
	}
	if in.DueDate != nil {
		if *in.DueDate == "" {
			t.DueDate = nil
		} else {
			d := *in.DueDate
			t.DueDate = &d
		}
	}
	t.UpdatedAt = now
}

func validateTitle(f fieldErrors, title string) {
	if n := utf8.RuneCountInString(strings.TrimSpace(title)); n < 1 || n > MaxTitleLength {
		f.add("title", fmt.Sprintf("must be 1-%d characters", MaxTitleLength))
	}
}

func validatePriority(f fieldErrors, p int) {
	if p < MinPriority || p > MaxPriority {
		f.add("priority", fmt.Sprintf("must be between %d and %d", MinPriority, MaxPriority))
	}
}

func validateDueDate(f fieldErrors, s string, emptyClears bool) {
	if s == "" && emptyClears {
		return
	}
	// time.DateOnly is "2006-01-02". Parsing (rather than a regexp) also rejects 2026-02-30.
	if _, err := time.Parse(time.DateOnly, s); err != nil {
		f.add("due_date", "must be a date in YYYY-MM-DD format")
	}
}
