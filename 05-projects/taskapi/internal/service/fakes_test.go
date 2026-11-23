package service

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/auth"
	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// fakeStore is an in-memory implementation of UserRepo, ProjectRepo and TaskRepo. It mimics the
// contract of the real store (owner scoping, conflicts, ErrNotFound) closely enough that the
// service tests exercise real business rules without a database.
type fakeStore struct {
	mu       sync.Mutex
	users    map[string]domain.User // by id
	projects map[string]domain.Project
	tasks    map[string]domain.Task

	// auth tables (see fakes_auth_test.go)
	accounts map[string]domain.Account // by id
	sessions map[string]domain.Session // by id
	verifs   map[string]domain.Verification
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		users:    map[string]domain.User{},
		projects: map[string]domain.Project{},
		tasks:    map[string]domain.Task{},
		accounts: map[string]domain.Account{},
		sessions: map[string]domain.Session{},
		verifs:   map[string]domain.Verification{},
	}
}

func (f *fakeStore) CreateUserWithAccount(_ context.Context, u domain.User, a domain.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.users {
		if existing.Email == u.Email {
			return domain.ErrConflict
		}
	}
	f.users[u.ID] = u
	f.accounts[a.ID] = a
	return nil
}

func (f *fakeStore) GetUserByEmail(_ context.Context, email string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, u := range f.users {
		if u.Email == email {
			return u, nil
		}
	}
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeStore) GetUserByID(_ context.Context, id string) (domain.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if u, ok := f.users[id]; ok {
		return u, nil
	}
	return domain.User{}, domain.ErrNotFound
}

func (f *fakeStore) CreateProject(_ context.Context, p domain.Project) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.projects {
		if existing.OwnerID == p.OwnerID && existing.Name == p.Name {
			return domain.ErrConflict
		}
	}
	f.projects[p.ID] = p
	return nil
}

func (f *fakeStore) GetProject(_ context.Context, ownerID, id string) (domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok || p.OwnerID != ownerID {
		return domain.Project{}, domain.ErrNotFound
	}
	return p, nil
}

func (f *fakeStore) ListProjects(_ context.Context, ownerID, afterID string, limit int) ([]domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Project
	for _, p := range f.projects {
		if p.OwnerID == ownerID && p.ID > afterID {
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b domain.Project) int { return strings.Compare(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeStore) RenameProject(_ context.Context, ownerID, id, name string, at time.Time) (domain.Project, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok || p.OwnerID != ownerID {
		return domain.Project{}, domain.ErrNotFound
	}
	p.Name, p.UpdatedAt = name, at
	f.projects[id] = p
	return p, nil
}

func (f *fakeStore) DeleteProject(_ context.Context, ownerID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	p, ok := f.projects[id]
	if !ok || p.OwnerID != ownerID {
		return domain.ErrNotFound
	}
	delete(f.projects, id)
	for tid, t := range f.tasks { // emulate ON DELETE CASCADE
		if t.ProjectID == id {
			delete(f.tasks, tid)
		}
	}
	return nil
}

// ownsTask must be called with f.mu held.
func (f *fakeStore) ownsTask(ownerID string, t domain.Task) bool {
	p, ok := f.projects[t.ProjectID]
	return ok && p.OwnerID == ownerID
}

func (f *fakeStore) CreateTask(_ context.Context, t domain.Task) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.projects[t.ProjectID]; !ok {
		return domain.ErrNotFound
	}
	f.tasks[t.ID] = t
	return nil
}

func (f *fakeStore) GetTask(_ context.Context, ownerID, id string) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok || !f.ownsTask(ownerID, t) {
		return domain.Task{}, domain.ErrNotFound
	}
	return t, nil
}

func (f *fakeStore) ListTasks(_ context.Context, projectID string, status *domain.TaskStatus, afterID string, limit int) ([]domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Task
	for _, t := range f.tasks {
		if t.ProjectID == projectID && t.ID > afterID && (status == nil || t.Status == *status) {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b domain.Task) int { return strings.Compare(a.ID, b.ID) })
	return out[:min(limit, len(out))], nil
}

func (f *fakeStore) UpdateTask(_ context.Context, ownerID, id string, apply func(*domain.Task) error) (domain.Task, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok || !f.ownsTask(ownerID, t) {
		return domain.Task{}, domain.ErrNotFound
	}
	if err := apply(&t); err != nil {
		return domain.Task{}, err
	}
	f.tasks[id] = t
	return t, nil
}

func (f *fakeStore) DeleteTask(_ context.Context, ownerID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	t, ok := f.tasks[id]
	if !ok || !f.ownsTask(ownerID, t) {
		return domain.ErrNotFound
	}
	delete(f.tasks, id)
	return nil
}

// fakeHasher is a deliberately trivial, instant "hash" (and records calls) so service tests do
// not pay for Argon2id. The real hasher is covered by the auth package's tests. Hashes starting
// with "old:" simulate an outdated parameter set (needsRehash), "legacy:" a bcrypt-era hash.
type fakeHasher struct {
	mu       sync.Mutex
	verifies int
}

func (h *fakeHasher) Hash(p string) (string, error) { return "hashed:" + p, nil }

func (h *fakeHasher) Verify(p, hash string) (ok, needsRehash bool, err error) {
	h.mu.Lock()
	h.verifies++
	h.mu.Unlock()
	switch {
	case strings.HasPrefix(hash, "legacy:"):
		return false, false, auth.ErrUnsupportedAlgorithm
	case strings.HasPrefix(hash, "garbage"):
		return false, false, auth.ErrInvalidHash
	case hash == "hashed:"+p:
		return true, false, nil
	case hash == "old:"+p:
		return true, true, nil
	}
	return false, false, nil
}

type fakeTokens struct{}

func (fakeTokens) Issue(userID string) (string, time.Time, error) {
	return "token-for-" + userID, time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC), nil
}

// seqIDs returns a deterministic, increasing id generator: "id-001", "id-002", ...
func seqIDs() idGen {
	var mu sync.Mutex
	n := 0
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		n++
		return fmt.Sprintf("id-%03d", n)
	}
}

var fixedNow = time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)

func fixedClock() time.Time { return fixedNow }
