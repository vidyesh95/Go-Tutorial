// Package store is lesson 11: a small data-access layer (TaskStore) that is easy to test
// against a REAL database. Run the tests with:
//
//	go test ./03-sql-sqlite/11testing-db/ -v
//
// Why test against a real database instead of mocks?
//   - The bugs live in the SQL: wrong column, missing WHERE, constraint violation, NULL handling.
//     A mock of *sql.DB only proves you called the mock you set up.
//   - SQLite is embedded and starts in microseconds, so each test can have its OWN fresh
//     database file. No shared state means tests can run in parallel and in any order.
//
// Testing recipe (see store_test.go):
//   - newTestDB(t) builds an isolated database: t.TempDir() gives a directory that Go deletes
//     automatically, and t.Cleanup(db.Close) closes the handle first (cleanups run last-in
//     first-out). Helpers take testing.TB and call t.Helper() so failures point at the test.
//   - Table-driven tests describe cases as data and run each as a t.Run subtest.
//   - t.Context() (Go 1.24+) is a context cancelled when the test ends, so database calls
//     never outlive the test.
//   - Map low-level errors to domain errors (sql.ErrNoRows becomes ErrNotFound, so callers need
//     not import database/sql) and test that mapping with errors.Is.
//
// Gotchas:
//   - Use a temp FILE (or a single-connection pool) for SQLite, not ":memory:" with a pool:
//     each pooled connection would get a different empty database (see internal/sqldb).
//   - SQLite is not PostgreSQL. Dialect-specific behaviour (locking, types, error codes) can
//     differ, so critical production paths also deserve a test against the real engine.
//
// In industry: unit tests use fakes for business logic, and repository/store tests run against
// the real database engine, typically PostgreSQL in Docker via testcontainers-go or a CI
// service container, with the schema applied from the same migrations production uses.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

// ErrNotFound is returned when a row does not exist. Callers (an HTTP handler, say) check it with
// errors.Is and return 404 without importing database/sql.
var ErrNotFound = errors.New("not found")

// Task is the domain type returned by the store.
type Task struct {
	ID         int64
	ProjectID  int64
	AssigneeID *int64 // nil = unassigned
	Title      string
	Status     string
	Priority   int
}

// NewTask is the input for Create. Zero Status/Priority fall back to the column defaults.
type NewTask struct {
	ProjectID  int64
	AssigneeID *int64
	Title      string
	Status     string // "" = default ('todo')
	Priority   int    // 0 = default (3)
}

// TaskStore reads and writes tasks.
type TaskStore struct {
	db *sql.DB
}

func NewTaskStore(db *sql.DB) *TaskStore { return &TaskStore{db: db} }

const taskColumns = `id, project_id, assignee_id, title, status, priority`

func scanTask(s interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := s.Scan(&t.ID, &t.ProjectID, &t.AssigneeID, &t.Title, &t.Status, &t.Priority)
	return t, err
}

// Create inserts a task and returns it as stored (defaults filled in by the database).
func (s *TaskStore) Create(ctx context.Context, in NewTask) (Task, error) {
	// COALESCE(NULLIF(?, ''), 'todo') lets Go's zero value mean "use the default".
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO tasks (project_id, assignee_id, title, status, priority)
		 VALUES (?, ?, ?, COALESCE(NULLIF(?, ''), 'todo'), COALESCE(NULLIF(?, 0), 3))
		 RETURNING `+taskColumns,
		in.ProjectID, in.AssigneeID, in.Title, in.Status, in.Priority)
	t, err := scanTask(row)
	if err != nil {
		return Task{}, fmt.Errorf("create task: %w", err)
	}
	return t, nil
}

// Get returns one task, or an error wrapping ErrNotFound.
func (s *TaskStore) Get(ctx context.Context, id int64) (Task, error) {
	t, err := scanTask(s.db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		// %w twice: callers can match ErrNotFound, and the original cause is still in the chain.
		return Task{}, fmt.Errorf("task %d: %w (%w)", id, ErrNotFound, err)
	}
	if err != nil {
		return Task{}, fmt.Errorf("get task %d: %w", id, err)
	}
	return t, nil
}

// ListByProject returns a project's tasks ordered by priority then id. An unknown or empty
// project yields an empty slice, not an error.
func (s *TaskStore) ListByProject(ctx context.Context, projectID int64) ([]Task, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT `+taskColumns+` FROM tasks WHERE project_id = ? ORDER BY priority, id`, projectID)
	if err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	defer rows.Close()
	tasks := []Task{}
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, fmt.Errorf("scan task: %w", err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list tasks: %w", err)
	}
	return tasks, nil
}
