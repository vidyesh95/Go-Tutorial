package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

func newAuthSvc(t *testing.T, store *fakeStore) (*AuthService, *fakeHasher) {
	t.Helper()
	h := &fakeHasher{}
	svc, err := NewAuthService(store, store, h, fakeTokens{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	svc.now, svc.newID = fixedClock, seqIDs()
	return svc, h
}

func TestSignup(t *testing.T) {
	t.Parallel()
	good := domain.SignupInput{Email: "  Ada@Example.com ", Name: " Ada ", Password: "correct horse"}

	tests := []struct {
		name    string
		in      domain.SignupInput
		prep    func(*fakeStore)
		wantErr error // nil = success; domain.ErrConflict, or a *ValidationError via wantVal
		wantVal bool
	}{
		{name: "ok, normalized", in: good},
		{name: "validation error", in: domain.SignupInput{Email: "bad"}, wantVal: true},
		{
			name: "duplicate email (case-insensitive)", in: good, wantErr: domain.ErrConflict,
			prep: func(s *fakeStore) { s.users["x"] = domain.User{ID: "x", Email: "ada@example.com"} },
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			store := newFakeStore()
			if tt.prep != nil {
				tt.prep(store)
			}
			svc, _ := newAuthSvc(t, store)
			u, err := svc.Signup(t.Context(), tt.in)
			switch {
			case tt.wantVal:
				if _, ok := errors.AsType[*domain.ValidationError](err); !ok {
					t.Fatalf("got %v, want *ValidationError", err)
				}
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("got %v, want %v", err, tt.wantErr)
				}
			default:
				if err != nil {
					t.Fatal(err)
				}
				if u.Email != "ada@example.com" || u.Name != "Ada" || u.ID != "id-001" {
					t.Fatalf("unexpected user %+v", u)
				}
				if !u.CreatedAt.Equal(fixedNow) || !u.UpdatedAt.Equal(fixedNow) || u.EmailVerified {
					t.Fatalf("timestamps/verified = %v %v %v", u.CreatedAt, u.UpdatedAt, u.EmailVerified)
				}
				// The password lives in the credential account, hashed, never in the user.
				acc, err := store.GetCredentialAccount(t.Context(), u.ID)
				if err != nil || acc.Password == nil || *acc.Password != "hashed:correct horse" || acc.AccountID != u.ID || acc.ID == u.ID {
					t.Fatalf("credential account = %+v, %v", acc, err)
				}
			}
		})
	}
}

func TestLogin(t *testing.T) {
	t.Parallel()
	store := newFakeStore()
	svc, hasher := newAuthSvc(t, store)
	if _, err := svc.Signup(t.Context(), domain.SignupInput{Email: "ada@example.com", Name: "Ada", Password: "correct horse"}); err != nil {
		t.Fatal(err)
	}

	res, err := svc.Login(t.Context(), " ADA@example.com", "correct horse")
	if err != nil || res.Token != "token-for-id-001" {
		t.Fatalf("login = %+v, %v", res, err)
	}

	before := hasher.verifies
	for _, tc := range []struct{ name, email, pw string }{
		{"wrong password", "ada@example.com", "nope"},
		{"unknown email", "ghost@example.com", "correct horse"},
		{"oversized password", "ada@example.com", string(make([]byte, 5000))},
	} {
		_, err := svc.Login(t.Context(), tc.email, tc.pw)
		if !errors.Is(err, domain.ErrUnauthorized) {
			t.Errorf("%s: got %v, want ErrUnauthorized", tc.name, err)
		}
	}
	// wrong password and unknown email must both have run a full Argon2id-style verification
	// (timing equalization); the oversized password is rejected before any hashing work.
	if got := hasher.verifies - before; got != 2 {
		t.Errorf("verify calls = %d, want 2", got)
	}
}

// Rehash on login, OAuth-only accounts, legacy hashes: the cases around Authenticate.
func TestAuthenticateEdgeCases(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newFakeStore()
	svc, hasher := newAuthSvc(t, store)
	u, err := svc.Signup(ctx, domain.SignupInput{Email: "ada@example.com", Name: "Ada", Password: "correct horse"})
	if err != nil {
		t.Fatal(err)
	}
	acc, _ := store.GetCredentialAccount(ctx, u.ID)

	// 1. A hash made with outdated parameters is replaced after a successful login...
	old := "old:correct horse"
	store.accounts[acc.ID] = withPassword(acc, &old)
	if _, err := svc.Authenticate(ctx, "ada@example.com", "correct horse"); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.GetCredentialAccount(ctx, u.ID); *got.Password != "hashed:correct horse" {
		t.Fatalf("hash not upgraded on login: %q", *got.Password)
	}
	// ...but a WRONG password must not touch it (we do not have the plaintext).
	store.accounts[acc.ID] = withPassword(acc, &old)
	if _, err := svc.Authenticate(ctx, "ada@example.com", "wrong"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatal(err)
	}
	if got, _ := store.GetCredentialAccount(ctx, u.ID); *got.Password != old {
		t.Fatalf("failed login changed the hash: %q", *got.Password)
	}

	// 2. A legacy (bcrypt) hash cannot log in; it is a 401, not a 500, and costs the same time.
	legacy := "legacy:$2a$12$whatever"
	store.accounts[acc.ID] = withPassword(acc, &legacy)
	before := hasher.verifies
	if _, err := svc.Authenticate(ctx, "ada@example.com", "correct horse"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("legacy hash: got %v, want ErrUnauthorized", err)
	}
	if hasher.verifies-before < 2 { // the attempt itself + the timing-equalizing dummy verify
		t.Errorf("legacy hash path did not equalize timing (%d verifies)", hasher.verifies-before)
	}

	// 3. A genuinely corrupt hash is a server fault, not "wrong password".
	garbage := "garbage"
	store.accounts[acc.ID] = withPassword(acc, &garbage)
	if _, err := svc.Authenticate(ctx, "ada@example.com", "correct horse"); err == nil || errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("corrupt hash: got %v, want a non-401 error", err)
	}

	// 4. A user without a credential account (GitHub only) cannot password-login.
	store.accounts[acc.ID] = withPassword(acc, nil)
	if _, err := svc.Authenticate(ctx, "ada@example.com", "correct horse"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("passwordless account: %v", err)
	}
	delete(store.accounts, acc.ID)
	if _, err := svc.Authenticate(ctx, "ada@example.com", "correct horse"); !errors.Is(err, domain.ErrUnauthorized) {
		t.Fatalf("no credential account: %v", err)
	}
}

func withPassword(a domain.Account, pw *string) domain.Account {
	a.Password = pw
	return a
}

func TestProjectsLifecycleAndIsolation(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newFakeStore()
	svc := NewProjectService(store)
	svc.now, svc.newID = fixedClock, seqIDs()

	p, err := svc.Create(ctx, "alice", "  Roadmap ")
	if err != nil || p.Name != "Roadmap" || p.OwnerID != "alice" {
		t.Fatalf("create = %+v, %v", p, err)
	}
	if _, err := svc.Create(ctx, "alice", "Roadmap"); !errors.Is(err, domain.ErrConflict) {
		t.Errorf("duplicate: %v", err)
	}
	if _, err := svc.Create(ctx, "bob", "Roadmap"); err != nil {
		t.Errorf("same name, other owner: %v", err)
	}
	if _, err := svc.Create(ctx, "alice", "  "); err == nil {
		t.Error("blank name accepted")
	}

	// Bob cannot see, rename or delete Alice's project; all look like "not found".
	if _, err := svc.Get(ctx, "bob", p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get: %v", err)
	}
	if _, err := svc.Rename(ctx, "bob", p.ID, "Mine"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("rename: %v", err)
	}
	if err := svc.Delete(ctx, "bob", p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("delete: %v", err)
	}

	renamed, err := svc.Rename(ctx, "alice", p.ID, "Plan")
	if err != nil || renamed.Name != "Plan" {
		t.Fatalf("rename = %+v, %v", renamed, err)
	}
	if err := svc.Delete(ctx, "alice", p.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, "alice", p.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}

func TestProjectPagination(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	svc := NewProjectService(newFakeStore())
	svc.newID = seqIDs()
	for i := range 5 {
		if _, err := svc.Create(ctx, "alice", fmt.Sprintf("p%d", i)); err != nil {
			t.Fatal(err)
		}
	}

	page1, err := svc.List(ctx, "alice", "", 2)
	if err != nil || len(page1.Items) != 2 || !page1.HasMore {
		t.Fatalf("page1 = %+v, %v", page1, err)
	}
	page2, _ := svc.List(ctx, "alice", page1.Items[1].ID, 2)
	page3, _ := svc.List(ctx, "alice", page2.Items[1].ID, 2)
	if len(page2.Items) != 2 || !page2.HasMore || len(page3.Items) != 1 || page3.HasMore {
		t.Fatalf("page2 = %+v page3 = %+v", page2, page3)
	}
	// Exactly-full last page must NOT claim there is more.
	exact, _ := svc.List(ctx, "alice", "", 5)
	if len(exact.Items) != 5 || exact.HasMore {
		t.Fatalf("exact = %+v", exact)
	}
	if other, _ := svc.List(ctx, "bob", "", 10); len(other.Items) != 0 {
		t.Fatalf("bob sees %d of alice's projects", len(other.Items))
	}
}

func newTaskSvc(store *fakeStore) *TaskService {
	svc := NewTaskService(store, store)
	svc.now, svc.newID = fixedClock, seqIDs()
	return svc
}

func TestTaskCreateRules(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newFakeStore()
	projects := NewProjectService(store)
	p, _ := projects.Create(ctx, "alice", "P")
	svc := newTaskSvc(store)

	task, err := svc.Create(ctx, "alice", p.ID, domain.CreateTaskInput{Title: " Write tests "})
	if err != nil {
		t.Fatal(err)
	}
	if task.Title != "Write tests" || task.Status != domain.StatusTodo || task.Priority != domain.DefaultPriority {
		t.Fatalf("defaults not applied: %+v", task)
	}

	if _, err := svc.Create(ctx, "bob", p.ID, domain.CreateTaskInput{Title: "sneaky"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bob creating in alice's project: %v", err)
	}
	if _, err := svc.Create(ctx, "alice", "missing", domain.CreateTaskInput{Title: "x"}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing project: %v", err)
	}
	if _, err := svc.Create(ctx, "alice", p.ID, domain.CreateTaskInput{}); err == nil {
		t.Error("empty title accepted")
	}
}

func TestTaskListFilterAndOwnership(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newFakeStore()
	p, _ := NewProjectService(store).Create(ctx, "alice", "P")
	svc := newTaskSvc(store)
	for i := range 4 {
		task, _ := svc.Create(ctx, "alice", p.ID, domain.CreateTaskInput{Title: fmt.Sprint("t", i)})
		if i%2 == 0 {
			st := domain.StatusDone
			if _, err := svc.Update(ctx, "alice", task.ID, domain.UpdateTaskInput{Status: &st}); err != nil {
				t.Fatal(err)
			}
		}
	}

	done := domain.StatusDone
	page, err := svc.List(ctx, "alice", p.ID, ListTasksParams{Status: &done})
	if err != nil || len(page.Items) != 2 {
		t.Fatalf("done tasks = %d, %v", len(page.Items), err)
	}
	bogus := domain.TaskStatus("bogus")
	if _, err := svc.List(ctx, "alice", p.ID, ListTasksParams{Status: &bogus}); err == nil {
		t.Error("bogus status accepted")
	}
	if _, err := svc.List(ctx, "bob", p.ID, ListTasksParams{}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bob listing alice's tasks: %v", err)
	}
}

func TestTaskUpdateGetDelete(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	store := newFakeStore()
	p, _ := NewProjectService(store).Create(ctx, "alice", "P")
	svc := newTaskSvc(store)
	task, _ := svc.Create(ctx, "alice", p.ID, domain.CreateTaskInput{Title: "orig"})

	title, due := " renamed ", "2026-09-01"
	got, err := svc.Update(ctx, "alice", task.ID, domain.UpdateTaskInput{Title: &title, DueDate: &due})
	if err != nil || got.Title != "renamed" || got.DueDate == nil || *got.DueDate != due || got.Priority != domain.DefaultPriority {
		t.Fatalf("update = %+v, %v", got, err)
	}
	if _, err := svc.Update(ctx, "alice", task.ID, domain.UpdateTaskInput{}); err == nil {
		t.Error("empty patch accepted")
	}
	if _, err := svc.Update(ctx, "bob", task.ID, domain.UpdateTaskInput{Title: &title}); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bob update: %v", err)
	}
	if _, err := svc.Get(ctx, "bob", task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bob get: %v", err)
	}
	if err := svc.Delete(ctx, "bob", task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("bob delete: %v", err)
	}
	if err := svc.Delete(ctx, "alice", task.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(ctx, "alice", task.ID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("get after delete: %v", err)
	}
}
