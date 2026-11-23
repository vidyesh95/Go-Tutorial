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

// UserRepo persists users. Create methods return domain.ErrConflict on a uniqueness violation
// and getters return domain.ErrNotFound for a missing row.
type UserRepo interface {
	CreateUser(ctx context.Context, u domain.User) error
	GetUserByEmail(ctx context.Context, email string) (domain.User, error)
	GetUserByID(ctx context.Context, id string) (domain.User, error)
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
