// Package store is the persistence layer: it implements the repository interfaces that the
// service package declares, on top of SQLite (via database/sql) and sqlc-generated queries.
//
// Its two jobs besides running SQL:
//   - translate between sqlc's row types (strings, int64) and domain types (time.Time, TaskStatus);
//   - translate driver errors into domain errors, so no other package ever imports database/sql
//     or the SQLite driver to interpret a failure.
package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"time"

	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib" // result-code constants

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/store/sqlcdb"
)

// timeLayout is how timestamps are stored: fixed-width UTC, millisecond precision.
const timeLayout = "2006-01-02T15:04:05.000Z"

// DSN builds the connection string. Pragmas listed here run on EVERY connection the pool opens
// (they are per-connection settings in SQLite):
//   - foreign_keys(1): off by default; without it ON DELETE CASCADE and REFERENCES do nothing.
//   - journal_mode(WAL): readers do not block the writer and vice versa.
//   - synchronous(NORMAL): the standard pairing with WAL; durable across app crashes, and loses
//     at most the last transactions on an OS crash or power cut, never corrupts the database.
//   - busy_timeout(5000): a blocked writer waits up to 5s instead of failing with "database is
//     locked".
//   - _txlock=immediate: transactions start with BEGIN IMMEDIATE, taking the write lock up
//     front. A read-then-write transaction that starts as a plain (deferred) read can fail
//     with SQLITE_BUSY when it tries to upgrade, and busy_timeout cannot help with that.
func DSN(path string) string {
	q := url.Values{}
	q.Add("_pragma", "foreign_keys(1)")
	q.Add("_pragma", "journal_mode(WAL)")
	q.Add("_pragma", "synchronous(NORMAL)")
	q.Add("_pragma", "busy_timeout(5000)")
	q.Set("_txlock", "immediate")
	return "file:" + path + "?" + q.Encode()
}

// Open opens the database file (creating it if needed) and verifies it is reachable. sql.Open
// alone never connects, so Ping makes misconfiguration fail at startup instead of on request 1.
func Open(ctx context.Context, path string) (*sql.DB, error) {
	db, err := sql.Open("sqlite", DSN(path))
	if err != nil {
		return nil, fmt.Errorf("store: open %s: %w", path, err)
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, fmt.Errorf("store: ping %s: %w", path, err)
	}
	return db, nil
}

// Store implements the service layer's UserRepo, ProjectRepo and TaskRepo.
type Store struct {
	db *sql.DB // nil inside a transaction (see WithTx)
	q  *sqlcdb.Queries
}

// New wraps an open database. The caller owns db and closes it.
func New(db *sql.DB) *Store {
	return &Store{db: db, q: sqlcdb.New(db)}
}

// WithTx runs fn inside a transaction: committed if fn returns nil, rolled back otherwise
// (including on panic, which is re-raised). fn receives a Store bound to the transaction, so
// everything it does is atomic.
func (s *Store) WithTx(ctx context.Context, fn func(tx *Store) error) (err error) {
	if s.db == nil {
		return errors.New("store: nested transaction")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("store: begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			tx.Rollback()
			panic(p)
		}
	}()
	if err := fn(&Store{q: s.q.WithTx(tx)}); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			return errors.Join(err, fmt.Errorf("store: rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("store: commit: %w", mapError(err))
	}
	return nil
}

// --- Users --------------------------------------------------------------------------------

func (s *Store) CreateUser(ctx context.Context, u domain.User) error {
	err := s.q.CreateUser(ctx, sqlcdb.CreateUserParams{
		ID: u.ID, Email: u.Email, Name: u.Name, PasswordHash: u.PasswordHash,
		CreatedAt: formatTime(u.CreatedAt),
	})
	return wrap("create user", err)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (domain.User, error) {
	row, err := s.q.GetUserByEmail(ctx, email)
	if err != nil {
		return domain.User{}, wrap("get user by email", err)
	}
	return toUser(row)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (domain.User, error) {
	row, err := s.q.GetUserByID(ctx, id)
	if err != nil {
		return domain.User{}, wrap("get user", err)
	}
	return toUser(row)
}

// --- Projects -----------------------------------------------------------------------------

func (s *Store) CreateProject(ctx context.Context, p domain.Project) error {
	err := s.q.CreateProject(ctx, sqlcdb.CreateProjectParams{
		ID: p.ID, OwnerID: p.OwnerID, Name: p.Name,
		CreatedAt: formatTime(p.CreatedAt), UpdatedAt: formatTime(p.UpdatedAt),
	})
	return wrap("create project", err)
}

func (s *Store) GetProject(ctx context.Context, ownerID, id string) (domain.Project, error) {
	row, err := s.q.GetProject(ctx, sqlcdb.GetProjectParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return domain.Project{}, wrap("get project", err)
	}
	return toProject(row)
}

func (s *Store) ListProjects(ctx context.Context, ownerID, afterID string, limit int) ([]domain.Project, error) {
	rows, err := s.q.ListProjects(ctx, sqlcdb.ListProjectsParams{
		OwnerID: ownerID, AfterID: afterID, RowLimit: int64(limit),
	})
	if err != nil {
		return nil, wrap("list projects", err)
	}
	out := make([]domain.Project, 0, len(rows))
	for _, r := range rows {
		p, err := toProject(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) RenameProject(ctx context.Context, ownerID, id, name string, at time.Time) (domain.Project, error) {
	row, err := s.q.RenameProject(ctx, sqlcdb.RenameProjectParams{
		Name: name, UpdatedAt: formatTime(at), ID: id, OwnerID: ownerID,
	})
	if err != nil {
		return domain.Project{}, wrap("rename project", err)
	}
	return toProject(row)
}

func (s *Store) DeleteProject(ctx context.Context, ownerID, id string) error {
	n, err := s.q.DeleteProject(ctx, sqlcdb.DeleteProjectParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return wrap("delete project", err)
	}
	return wrapAffected("delete project", n)
}

// --- Tasks --------------------------------------------------------------------------------

func (s *Store) CreateTask(ctx context.Context, t domain.Task) error {
	err := s.q.CreateTask(ctx, sqlcdb.CreateTaskParams{
		ID: t.ID, ProjectID: t.ProjectID, Title: t.Title, Status: string(t.Status),
		Priority: int64(t.Priority), DueDate: t.DueDate,
		CreatedAt: formatTime(t.CreatedAt), UpdatedAt: formatTime(t.UpdatedAt),
	})
	return wrap("create task", err)
}

func (s *Store) GetTask(ctx context.Context, ownerID, id string) (domain.Task, error) {
	row, err := s.q.GetTask(ctx, sqlcdb.GetTaskParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return domain.Task{}, wrap("get task", err)
	}
	return toTask(row)
}

// ListTasks returns up to limit tasks of a project with id > afterID, ordered by id. A nil
// status means "any status". The caller must already have verified project ownership.
func (s *Store) ListTasks(ctx context.Context, projectID string, status *domain.TaskStatus, afterID string, limit int) ([]domain.Task, error) {
	var st *string
	if status != nil {
		v := string(*status)
		st = &v
	}
	rows, err := s.q.ListTasks(ctx, sqlcdb.ListTasksParams{
		ProjectID: projectID, Status: st, AfterID: afterID, RowLimit: int64(limit),
	})
	if err != nil {
		return nil, wrap("list tasks", err)
	}
	out := make([]domain.Task, 0, len(rows))
	for _, r := range rows {
		t, err := toTask(r)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

// UpdateTask is an atomic read-modify-write: it loads the task (scoped to ownerID), lets apply
// change it (validation, patch semantics: that is business logic and lives in the service),
// and writes it back, all in one transaction. Two concurrent PATCHes therefore serialize
// instead of one silently overwriting the other's fields.
func (s *Store) UpdateTask(ctx context.Context, ownerID, id string, apply func(*domain.Task) error) (domain.Task, error) {
	var updated domain.Task
	err := s.WithTx(ctx, func(tx *Store) error {
		t, err := tx.GetTask(ctx, ownerID, id)
		if err != nil {
			return err
		}
		if err := apply(&t); err != nil {
			return err
		}
		row, err := tx.q.UpdateTask(ctx, sqlcdb.UpdateTaskParams{
			ID: t.ID, OwnerID: ownerID, Title: t.Title, Status: string(t.Status),
			Priority: int64(t.Priority), DueDate: t.DueDate, UpdatedAt: formatTime(t.UpdatedAt),
		})
		if err != nil {
			return wrap("update task", err)
		}
		updated, err = toTask(row)
		return err
	})
	return updated, err
}

func (s *Store) DeleteTask(ctx context.Context, ownerID, id string) error {
	n, err := s.q.DeleteTask(ctx, sqlcdb.DeleteTaskParams{ID: id, OwnerID: ownerID})
	if err != nil {
		return wrap("delete task", err)
	}
	return wrapAffected("delete task", n)
}

// --- Errors -------------------------------------------------------------------------------

// mapError converts driver-level errors into domain errors. Anything unrecognised is returned
// unchanged and ends up as a 500 (with the real cause in the server log).
func mapError(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return domain.ErrNotFound
	}
	if se, ok := errors.AsType[*sqlite.Error](err); ok {
		switch se.Code() {
		case sqlite3.SQLITE_CONSTRAINT_UNIQUE, sqlite3.SQLITE_CONSTRAINT_PRIMARYKEY:
			return domain.ErrConflict
		case sqlite3.SQLITE_CONSTRAINT_FOREIGNKEY:
			// E.g. creating a task whose project was deleted a moment ago: from the caller's
			// point of view the project does not exist.
			return domain.ErrNotFound
		}
	}
	return err
}

// wrap adds the operation name and maps the driver error. Because the domain sentinel is
// wrapped with %w, errors.Is(err, domain.ErrNotFound) works through any number of layers.
func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	mapped := mapError(err)
	if mapped != err {
		// Keep the driver error in the message for logs while exposing only the domain sentinel
		// to errors.Is.
		return fmt.Errorf("store: %s: %w (%v)", op, mapped, err)
	}
	return fmt.Errorf("store: %s: %w", op, err)
}

// wrapAffected turns "0 rows affected" into ErrNotFound for DELETEs scoped by owner.
func wrapAffected(op string, n int64) error {
	if n == 0 {
		return fmt.Errorf("store: %s: %w", op, domain.ErrNotFound)
	}
	return nil
}

// --- Row -> domain conversion ---------------------------------------------------------------

func formatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, error) {
	t, err := time.Parse(timeLayout, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("store: corrupt timestamp %q: %w", s, err)
	}
	return t, nil
}

func toUser(r sqlcdb.User) (domain.User, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return domain.User{}, err
	}
	return domain.User{ID: r.ID, Email: r.Email, Name: r.Name, PasswordHash: r.PasswordHash, CreatedAt: created}, nil
}

func toProject(r sqlcdb.Project) (domain.Project, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return domain.Project{}, err
	}
	return domain.Project{ID: r.ID, OwnerID: r.OwnerID, Name: r.Name, CreatedAt: created, UpdatedAt: updated}, nil
}

func toTask(r sqlcdb.Task) (domain.Task, error) {
	created, err := parseTime(r.CreatedAt)
	if err != nil {
		return domain.Task{}, err
	}
	updated, err := parseTime(r.UpdatedAt)
	if err != nil {
		return domain.Task{}, err
	}
	return domain.Task{
		ID: r.ID, ProjectID: r.ProjectID, Title: r.Title, Status: domain.TaskStatus(r.Status),
		Priority: int(r.Priority), DueDate: r.DueDate, CreatedAt: created, UpdatedAt: updated,
	}, nil
}
