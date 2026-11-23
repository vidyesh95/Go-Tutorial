package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

// newTestDB returns an isolated, migrated database with one user (id 1) and one project (id 1).
// Because every test gets its own file, tests can use t.Parallel() safely.
func newTestDB(tb testing.TB) *sql.DB {
	tb.Helper() // report failures at the caller's line, not here

	// t.TempDir is removed automatically after the test and its cleanups finish.
	path := filepath.Join(tb.TempDir(), "test.db")
	db, err := sqldb.Open(tb.Context(), path)
	if err != nil {
		tb.Fatalf("open db: %v", err)
	}
	// Registered AFTER TempDir, so it runs BEFORE the directory is deleted (LIFO order).
	tb.Cleanup(func() { db.Close() })

	if err := sqldb.Schema(tb.Context(), db); err != nil {
		tb.Fatalf("apply schema: %v", err)
	}
	_, err = db.ExecContext(tb.Context(), `
		INSERT INTO users (id, email, name) VALUES (1, 'test@example.com', 'Test');
		INSERT INTO projects (id, owner_id, name) VALUES (1, 1, 'Test project');`)
	if err != nil {
		tb.Fatalf("seed: %v", err)
	}
	return db
}

func ptr[T any](v T) *T { return &v }

func TestTaskStore_Create(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      NewTask
		want    Task // ID is ignored (assigned by the database)
		wantErr bool
	}{
		{
			name: "defaults filled in by the database",
			in:   NewTask{ProjectID: 1, Title: "Write tests"},
			want: Task{ProjectID: 1, Title: "Write tests", Status: "todo", Priority: 3},
		},
		{
			name: "explicit values and assignee",
			in:   NewTask{ProjectID: 1, AssigneeID: ptr(int64(1)), Title: "Ship it", Status: "doing", Priority: 1},
			want: Task{ProjectID: 1, AssigneeID: ptr(int64(1)), Title: "Ship it", Status: "doing", Priority: 1},
		},
		{name: "empty title violates CHECK", in: NewTask{ProjectID: 1, Title: ""}, wantErr: true},
		{name: "bad status violates CHECK", in: NewTask{ProjectID: 1, Title: "x", Status: "blocked"}, wantErr: true},
		{name: "unknown project violates FOREIGN KEY", in: NewTask{ProjectID: 99, Title: "x"}, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := NewTaskStore(newTestDB(t))

			got, err := store.Create(t.Context(), tt.in)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("Create() = %+v, want an error", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if got.ID == 0 {
				t.Error("expected a database-assigned ID")
			}
			got.ID = 0 // compare everything except the generated ID
			if !equalTask(got, tt.want) {
				t.Errorf("Create() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestTaskStore_Get(t *testing.T) {
	t.Parallel()
	store := NewTaskStore(newTestDB(t))

	created, err := store.Create(t.Context(), NewTask{ProjectID: 1, Title: "Find me"})
	if err != nil {
		t.Fatal(err)
	}

	t.Run("found", func(t *testing.T) {
		got, err := store.Get(t.Context(), created.ID)
		if err != nil {
			t.Fatalf("Get() error = %v", err)
		}
		if !equalTask(got, created) {
			t.Errorf("Get() = %+v, want %+v", got, created)
		}
	})

	t.Run("not found maps to ErrNotFound", func(t *testing.T) {
		_, err := store.Get(t.Context(), 12345)
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("error = %v, want errors.Is(err, ErrNotFound)", err)
		}
		// The original cause is kept in the chain for logs/debugging.
		if !errors.Is(err, sql.ErrNoRows) {
			t.Errorf("error = %v, want it to also wrap sql.ErrNoRows", err)
		}
	})
}

func TestTaskStore_ListByProject(t *testing.T) {
	t.Parallel()
	db := newTestDB(t)
	store := NewTaskStore(db)
	ctx := t.Context()

	// A second project to prove the filter works.
	if _, err := db.ExecContext(ctx, `INSERT INTO projects (id, owner_id, name) VALUES (2, 1, 'Other')`); err != nil {
		t.Fatal(err)
	}
	for _, in := range []NewTask{
		{ProjectID: 1, Title: "low", Priority: 5},
		{ProjectID: 1, Title: "high", Priority: 1},
		{ProjectID: 1, Title: "also high", Priority: 1},
		{ProjectID: 2, Title: "elsewhere", Priority: 1},
	} {
		if _, err := store.Create(ctx, in); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		name      string
		projectID int64
		wantTitle []string // in order
	}{
		{"ordered by priority then id", 1, []string{"high", "also high", "low"}},
		{"other project only sees its own", 2, []string{"elsewhere"}},
		{"empty project returns empty slice, not an error", 3, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := store.ListByProject(ctx, tt.projectID)
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != len(tt.wantTitle) {
				t.Fatalf("got %d tasks, want %d: %+v", len(got), len(tt.wantTitle), got)
			}
			for i, task := range got {
				if task.Title != tt.wantTitle[i] {
					t.Errorf("task[%d].Title = %q, want %q", i, task.Title, tt.wantTitle[i])
				}
			}
		})
	}
}

func TestTaskStore_CancelledContext(t *testing.T) {
	t.Parallel()
	store := NewTaskStore(newTestDB(t))

	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancelled before the call
	if _, err := store.Get(ctx, 1); err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
}

func equalTask(a, b Task) bool {
	sameAssignee := (a.AssigneeID == nil && b.AssigneeID == nil) ||
		(a.AssigneeID != nil && b.AssigneeID != nil && *a.AssigneeID == *b.AssigneeID)
	return a.ID == b.ID && a.ProjectID == b.ProjectID && a.Title == b.Title &&
		a.Status == b.Status && a.Priority == b.Priority && sameAssignee
}
