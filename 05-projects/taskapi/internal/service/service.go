// Package service holds the application's business logic: validation, authorization rules
// ("you may only touch your own projects") and orchestration of repositories.
//
// The interfaces below are declared HERE, in the consumer, not next to the implementation
// (Go proverb: "accept interfaces, return structs"). The service states the minimum it needs;
// store.Store happens to satisfy them, and tests satisfy them with in-memory fakes. Because the
// interfaces are small and owned by the consumer, swapping SQLite for PostgreSQL later means
// writing a new implementation and changing one line of wiring in main.
package service

import (
	"context"
	"time"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// UserRepo persists users. Getters return domain.ErrNotFound for a missing row.
type UserRepo interface {
	// CreateUserWithAccount inserts the user and its first account atomically (a user with no
	// account could never sign in). domain.ErrConflict on a duplicate email.
	CreateUserWithAccount(ctx context.Context, u domain.User, a domain.Account) error
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	GetUserByID(ctx context.Context, id string) (domain.User, error)
	MarkEmailVerified(ctx context.Context, id string, at time.Time) error
}

// AccountRepo persists the ways a user can sign in (Better Auth's "account" table).
type AccountRepo interface {
	// GetCredentialAccount returns the email+password account, ErrNotFound if the user has none.
	GetCredentialAccount(ctx context.Context, userID string) (domain.Account, error)
	UpdatePasswordHash(ctx context.Context, accountID, hash string, at time.Time) error
	// ResetCredentialPassword sets the user's password (creating the credential account from a
	// if absent) and deletes ALL the user's sessions, atomically.
	ResetCredentialPassword(ctx context.Context, a domain.Account) error
	GetAccount(ctx context.Context, providerID, accountID string) (domain.Account, error)
	CreateAccount(ctx context.Context, a domain.Account) error
	UpdateAccountTokens(ctx context.Context, a domain.Account) error
}

// SessionRepo persists cookie sessions. Tokens never reach it: only their SHA-256 (TokenHash).
type SessionRepo interface {
	CreateSession(ctx context.Context, s domain.Session) error
	// GetSessionByTokenHash returns the session even if expired; the service decides.
	GetSessionByTokenHash(ctx context.Context, tokenHash string) (domain.Session, error)
	ExtendSession(ctx context.Context, id string, expiresAt, at time.Time) error
	ListActiveSessions(ctx context.Context, userID string, now time.Time) ([]domain.Session, error)
	// The first two deletes are idempotent (no error if nothing matched).
	DeleteSessionByTokenHash(ctx context.Context, tokenHash string) error
	DeleteSessionByID(ctx context.Context, id string) error
	// DeleteUserSession is ErrNotFound unless the session exists AND belongs to userID.
	DeleteUserSession(ctx context.Context, userID, id string) error
	DeleteOtherSessions(ctx context.Context, userID, keepID string) error
	DeleteExpiredSessions(ctx context.Context, now time.Time) (int64, error)
}

// VerificationRepo persists single-use emailed tokens (by hash).
type VerificationRepo interface {
	// ReplaceVerification stores v and deletes older rows with the same identifier.
	ReplaceVerification(ctx context.Context, v domain.Verification) error
	// ConsumeVerification atomically deletes and returns the identifier of an unexpired token of
	// the given purpose; ErrNotFound if unknown, used, expired or of another purpose.
	ConsumeVerification(ctx context.Context, purpose, valueHash string, now time.Time) (identifier string, err error)
	DeleteExpiredVerifications(ctx context.Context, now time.Time) (int64, error)
}

// Mailer sends one plain-text email. See package mailer.
type Mailer interface {
	Send(ctx context.Context, to, subject, body string) error
}

// ProjectRepo persists projects. Every method that takes an ownerID scopes the operation to that
// owner, so another user's project is reported as domain.ErrNotFound.
type ProjectRepo interface {
	CreateProject(ctx context.Context, p domain.Project) error
	GetProject(ctx context.Context, ownerID, id string) (domain.Project, error)
	ListProjects(ctx context.Context, ownerID, afterID string, limit int) ([]domain.Project, error)
	RenameProject(ctx context.Context, ownerID, id, name string, at time.Time) (domain.Project, error)
	DeleteProject(ctx context.Context, ownerID, id string) error
}

// TaskRepo persists tasks. Tasks are owned through their project.
type TaskRepo interface {
	CreateTask(ctx context.Context, t domain.Task) error
	GetTask(ctx context.Context, ownerID, id string) (domain.Task, error)
	// ListTasks does NOT check ownership; callers must have verified the project first.
	ListTasks(ctx context.Context, projectID string, status *domain.TaskStatus, afterID string, limit int) ([]domain.Task, error)
	// UpdateTask atomically loads the task, calls apply, and saves the result.
	UpdateTask(ctx context.Context, ownerID, id string, apply func(*domain.Task) error) (domain.Task, error)
	DeleteTask(ctx context.Context, ownerID, id string) error
}

// clock and idGen are the two sources of non-determinism. They are plain function fields so
// tests in this package can replace them without any mocking framework.
type (
	clock func() time.Time
	idGen func() string
)

// defaultClock truncates to milliseconds, the precision the database stores, so the value a
// create call returns is identical to what a later GET returns.
func defaultClock() time.Time { return time.Now().UTC().Truncate(time.Millisecond) }

// newID returns a UUIDv7 string: time-ordered, so ids sort by creation time and double as
// keyset-pagination cursors.
func newID() string { return uuid.NewV7().String() }

// paginate trims the "limit+1" rows the repository was asked for into a Page. Fetching one
// extra row is the cheapest way to learn whether another page exists, with no COUNT query.
func paginate[T any](rows []T, limit int) domain.Page[T] {
	if len(rows) > limit {
		return domain.Page[T]{Items: rows[:limit], HasMore: true}
	}
	return domain.Page[T]{Items: rows}
}
