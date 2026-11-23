package store

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// newTestStore returns a Store on a fresh, migrated database file. Each test gets its own file,
// so tests can run in parallel and in any order. (A file, not ":memory:": every pooled
// connection to ":memory:" would see its own empty database.)
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := Open(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { db.Close() }) // runs before TempDir removal (LIFO)
	if err := Migrate(t.Context(), db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return New(db)
}

// seq is atomic because parallel tests share it.
var seq atomic.Int64

// id returns strictly increasing, fixed-width ids. Real ids are UUIDv7; the store treats ids as
// opaque strings, so any ordered string works for these tests.
func id(prefix string) string {
	return fmt.Sprintf("%s-%06d", prefix, seq.Add(1))
}

var now = time.Date(2026, 3, 4, 5, 6, 7, 123_000_000, time.UTC)

func mustUser(t *testing.T, s *Store, email string) domain.User {
	t.Helper()
	u := domain.User{ID: id("u"), Email: email, Name: "Test", PasswordHash: "hash", CreatedAt: now}
	if err := s.CreateUser(t.Context(), u); err != nil {
		t.Fatalf("create user: %v", err)
	}
	return u
}

func mustProject(t *testing.T, s *Store, owner, name string) domain.Project {
	t.Helper()
	p := domain.Project{ID: id("p"), OwnerID: owner, Name: name, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateProject(t.Context(), p); err != nil {
		t.Fatalf("create project: %v", err)
	}
	return p
}

func mustTask(t *testing.T, s *Store, project, title string, status domain.TaskStatus) domain.Task {
	t.Helper()
	tk := domain.Task{ID: id("t"), ProjectID: project, Title: title, Status: status, Priority: 3, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(t.Context(), tk); err != nil {
		t.Fatalf("create task: %v", err)
	}
	return tk
}

func TestMigrateIsIdempotent(t *testing.T) {
	s := newTestStore(t)
	if err := Migrate(t.Context(), s.db); err != nil {
		t.Fatalf("second Migrate: %v", err)
	}
}

func TestUsers(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "ada@example.com")

	got, err := s.GetUserByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if got != u {
		t.Fatalf("round trip mismatch:\n got %+v\nwant %+v", got, u)
	}
	if got, err := s.GetUserByID(ctx, u.ID); err != nil || got.Email != u.Email {
		t.Fatalf("GetUserByID = %+v, %v", got, err)
	}

	if _, err := s.GetUserByEmail(ctx, "nobody@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("missing user: got %v, want ErrNotFound", err)
	}
	dup := domain.User{ID: id("u"), Email: "ada@example.com", Name: "Again", PasswordHash: "h", CreatedAt: now}
	if err := s.CreateUser(ctx, dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate email: got %v, want ErrConflict", err)
	}
}

func TestProjectsAreScopedToOwner(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	alice, bob := mustUser(t, s, "a@example.com"), mustUser(t, s, "b@example.com")
	p := mustProject(t, s, alice.ID, "Alice's")

	// Duplicate name for the same owner conflicts; the same name for another owner is fine.
	if err := s.CreateProject(ctx, domain.Project{ID: id("p"), OwnerID: alice.ID, Name: "Alice's", CreatedAt: now, UpdatedAt: now}); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate project name: got %v", err)
	}
	mustProject(t, s, bob.ID, "Alice's")

	if got, err := s.GetProject(ctx, alice.ID, p.ID); err != nil || got != p {
		t.Fatalf("owner get = %+v, %v", got, err)
	}
	// Every operation by the wrong owner looks exactly like "does not exist".
	if _, err := s.GetProject(ctx, bob.ID, p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get as bob: %v", err)
	}
	if _, err := s.RenameProject(ctx, bob.ID, p.ID, "mine now", now); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("rename as bob: %v", err)
	}
	if err := s.DeleteProject(ctx, bob.ID, p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete as bob: %v", err)
	}

	later := now.Add(time.Hour)
	renamed, err := s.RenameProject(ctx, alice.ID, p.ID, "Renamed", later)
	if err != nil || renamed.Name != "Renamed" || !renamed.UpdatedAt.Equal(later) {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if err := s.DeleteProject(ctx, alice.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.DeleteProject(ctx, alice.ID, p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete: %v", err)
	}
}

func TestListProjectsKeysetPagination(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "a@example.com")
	var want []string
	for i := range 5 {
		want = append(want, mustProject(t, s, u.ID, fmt.Sprintf("p%d", i)).ID)
	}

	var got []string
	after := ""
	for {
		page, err := s.ListProjects(ctx, u.ID, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, p := range page {
			got = append(got, p.ID)
		}
		after = page[len(page)-1].ID
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("pages = %v, want %v", got, want)
	}
}

func TestTasks(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	alice, bob := mustUser(t, s, "a@example.com"), mustUser(t, s, "b@example.com")
	p := mustProject(t, s, alice.ID, "P")

	due := "2026-12-31"
	tk := domain.Task{ID: id("t"), ProjectID: p.ID, Title: "with due", Status: domain.StatusTodo, Priority: 2, DueDate: &due, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, tk); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetTask(ctx, alice.ID, tk.ID)
	if err != nil || got.DueDate == nil || *got.DueDate != due || got.Priority != 2 {
		t.Fatalf("get = %+v, %v", got, err)
	}
	if _, err := s.GetTask(ctx, bob.ID, tk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get as bob: %v", err)
	}
	if err := s.DeleteTask(ctx, bob.ID, tk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete as bob: %v", err)
	}

	// Task for a project that does not exist: the FK violation maps to ErrNotFound.
	orphan := domain.Task{ID: id("t"), ProjectID: "missing", Title: "x", Status: domain.StatusTodo, Priority: 3, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, orphan); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("orphan task: got %v, want ErrNotFound", err)
	}
	// The CHECK constraint is the last line of defence behind domain validation.
	bad := domain.Task{ID: id("t"), ProjectID: p.ID, Title: "x", Status: "bogus", Priority: 3, CreatedAt: now, UpdatedAt: now}
	if err := s.CreateTask(ctx, bad); err == nil {
		t.Error("CHECK constraint did not reject invalid status")
	}
}

func TestListTasksStatusFilterAndPagination(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "a@example.com")
	p := mustProject(t, s, u.ID, "P")
	other := mustProject(t, s, u.ID, "Other")
	mustTask(t, s, other.ID, "elsewhere", domain.StatusTodo) // must never show up in P's list

	var todo []string
	for i := range 5 {
		todo = append(todo, mustTask(t, s, p.ID, fmt.Sprintf("todo %d", i), domain.StatusTodo).ID)
		mustTask(t, s, p.ID, fmt.Sprintf("done %d", i), domain.StatusDone)
	}

	all, err := s.ListTasks(ctx, p.ID, nil, "", 100)
	if err != nil || len(all) != 10 {
		t.Fatalf("unfiltered: %d tasks, %v", len(all), err)
	}

	status := domain.StatusTodo
	var got []string
	after := ""
	for {
		page, err := s.ListTasks(ctx, p.ID, &status, after, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) == 0 {
			break
		}
		for _, tk := range page {
			if tk.Status != domain.StatusTodo {
				t.Fatalf("filter leaked %+v", tk)
			}
			got = append(got, tk.ID)
		}
		after = page[len(page)-1].ID
	}
	if strings.Join(got, ",") != strings.Join(todo, ",") {
		t.Fatalf("filtered pages = %v, want %v", got, todo)
	}
}

func TestDeleteProjectCascadesToTasks(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "a@example.com")
	p := mustProject(t, s, u.ID, "P")
	tk := mustTask(t, s, p.ID, "doomed", domain.StatusTodo)

	if err := s.DeleteProject(ctx, u.ID, p.ID); err != nil {
		t.Fatal(err)
	}
	// Proves foreign_keys(1) is active on the pooled connection.
	if _, err := s.GetTask(ctx, u.ID, tk.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("task survived project deletion: %v", err)
	}
}

func TestUpdateTask(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	alice, bob := mustUser(t, s, "a@example.com"), mustUser(t, s, "b@example.com")
	p := mustProject(t, s, alice.ID, "P")
	tk := mustTask(t, s, p.ID, "before", domain.StatusTodo)

	got, err := s.UpdateTask(ctx, alice.ID, tk.ID, func(t *domain.Task) error {
		t.Title, t.Status = "after", domain.StatusDone
		return nil
	})
	if err != nil || got.Title != "after" || got.Status != domain.StatusDone {
		t.Fatalf("update = %+v, %v", got, err)
	}

	if _, err := s.UpdateTask(ctx, bob.ID, tk.ID, func(*domain.Task) error { return nil }); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("update as bob: %v", err)
	}

	// An error from apply aborts the write and is returned intact.
	sentinel := errors.New("rejected")
	_, err = s.UpdateTask(ctx, alice.ID, tk.ID, func(t *domain.Task) error { t.Title = "never saved"; return sentinel })
	if !errors.Is(err, sentinel) {
		t.Fatalf("got %v, want sentinel", err)
	}
	if now, _ := s.GetTask(ctx, alice.ID, tk.ID); now.Title != "after" {
		t.Fatalf("failed update was persisted: %+v", now)
	}
}

// The reason UpdateTask is a transaction: concurrent read-modify-write cycles must not lose
// updates. Each goroutine appends one character; a lost update would leave a shorter title.
func TestUpdateTaskConcurrentNoLostUpdates(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()
	u := mustUser(t, s, "a@example.com")
	p := mustProject(t, s, u.ID, "P")
	tk := mustTask(t, s, p.ID, "x", domain.StatusTodo)

	const workers = 20
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for range workers {
		wg.Go(func() {
			_, err := s.UpdateTask(ctx, u.ID, tk.ID, func(t *domain.Task) error {
				t.Title += "x"
				return nil
			})
			errs <- err
		})
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent update failed: %v", err)
		}
	}
	got, _ := s.GetTask(ctx, u.ID, tk.ID)
	if len(got.Title) != workers+1 {
		t.Fatalf("title length = %d, want %d (lost updates)", len(got.Title), workers+1)
	}
}

func TestWithTx(t *testing.T) {
	t.Parallel()
	s := newTestStore(t)
	ctx := t.Context()

	// Rollback on error: the user inserted inside the transaction must vanish.
	boom := errors.New("boom")
	err := s.WithTx(ctx, func(tx *Store) error {
		if err := tx.CreateUser(ctx, domain.User{ID: id("u"), Email: "rollback@example.com", Name: "n", PasswordHash: "h", CreatedAt: now}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("got %v, want boom", err)
	}
	if _, err := s.GetUserByEmail(ctx, "rollback@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("rolled-back row is visible: %v", err)
	}

	// Rollback on panic, and the panic propagates.
	func() {
		defer func() {
			if recover() == nil {
				t.Error("panic was swallowed")
			}
		}()
		_ = s.WithTx(ctx, func(tx *Store) error {
			_ = tx.CreateUser(ctx, domain.User{ID: id("u"), Email: "panic@example.com", Name: "n", PasswordHash: "h", CreatedAt: now})
			panic("oops")
		})
	}()
	if _, err := s.GetUserByEmail(ctx, "panic@example.com"); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("row from panicked tx is visible: %v", err)
	}

	// Commit on success.
	if err := s.WithTx(ctx, func(tx *Store) error {
		return tx.CreateUser(ctx, domain.User{ID: id("u"), Email: "commit@example.com", Name: "n", PasswordHash: "h", CreatedAt: now})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetUserByEmail(ctx, "commit@example.com"); err != nil {
		t.Fatalf("committed row missing: %v", err)
	}

	// Nesting is a programming error, reported rather than deadlocking.
	err = s.WithTx(ctx, func(tx *Store) error {
		return tx.WithTx(context.Background(), func(*Store) error { return nil })
	})
	if err == nil || !strings.Contains(err.Error(), "nested") {
		t.Fatalf("nested tx: got %v", err)
	}
}
