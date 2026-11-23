package service

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/vidyesh95/Go-Tutorial/05-projects/taskapi/internal/domain"
)

// In-memory AccountRepo, SessionRepo and VerificationRepo on fakeStore, honouring the same
// contract as the SQLite store (see the interfaces in service.go).

func (f *fakeStore) MarkEmailVerified(_ context.Context, id string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	u, ok := f.users[id]
	if !ok {
		return domain.ErrNotFound
	}
	u.EmailVerified, u.UpdatedAt = true, at
	f.users[id] = u
	return nil
}

// --- accounts ---

func (f *fakeStore) GetCredentialAccount(_ context.Context, userID string) (domain.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.accounts {
		if a.UserID == userID && a.ProviderID == domain.ProviderCredential {
			return a, nil
		}
	}
	return domain.Account{}, domain.ErrNotFound
}

func (f *fakeStore) UpdatePasswordHash(_ context.Context, accountID, hash string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	a, ok := f.accounts[accountID]
	if !ok {
		return domain.ErrNotFound
	}
	a.Password, a.UpdatedAt = &hash, at
	f.accounts[accountID] = a
	return nil
}

func (f *fakeStore) ResetCredentialPassword(_ context.Context, na domain.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.users[na.UserID]; !ok {
		return domain.ErrNotFound
	}
	found := false
	for id, a := range f.accounts {
		if a.UserID == na.UserID && a.ProviderID == domain.ProviderCredential {
			a.Password, a.UpdatedAt = na.Password, na.UpdatedAt
			f.accounts[id] = a
			found = true
		}
	}
	if !found {
		f.accounts[na.ID] = na
	}
	for id, s := range f.sessions {
		if s.UserID == na.UserID {
			delete(f.sessions, id)
		}
	}
	return nil
}

func (f *fakeStore) GetAccount(_ context.Context, providerID, accountID string) (domain.Account, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, a := range f.accounts {
		if a.ProviderID == providerID && a.AccountID == accountID {
			return a, nil
		}
	}
	return domain.Account{}, domain.ErrNotFound
}

func (f *fakeStore) CreateAccount(_ context.Context, a domain.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, e := range f.accounts {
		if e.ProviderID == a.ProviderID && e.AccountID == a.AccountID {
			return domain.ErrConflict
		}
	}
	f.accounts[a.ID] = a
	return nil
}

func (f *fakeStore) UpdateAccountTokens(_ context.Context, a domain.Account) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if _, ok := f.accounts[a.ID]; !ok {
		return domain.ErrNotFound
	}
	f.accounts[a.ID] = a
	return nil
}

// --- sessions ---

func (f *fakeStore) CreateSession(_ context.Context, s domain.Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sessions[s.ID] = s
	return nil
}

func (f *fakeStore) GetSessionByTokenHash(_ context.Context, hash string) (domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.sessions {
		if s.TokenHash == hash {
			return s, nil
		}
	}
	return domain.Session{}, domain.ErrNotFound
}

func (f *fakeStore) ExtendSession(_ context.Context, id string, expiresAt, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.sessions[id]
	if !ok {
		return domain.ErrNotFound
	}
	s.ExpiresAt, s.UpdatedAt = expiresAt, at
	f.sessions[id] = s
	return nil
}

func (f *fakeStore) ListActiveSessions(_ context.Context, userID string, now time.Time) ([]domain.Session, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []domain.Session
	for _, s := range f.sessions {
		if s.UserID == userID && s.ExpiresAt.After(now) {
			out = append(out, s)
		}
	}
	slices.SortFunc(out, func(a, b domain.Session) int { return strings.Compare(b.ID, a.ID) })
	return out, nil
}

func (f *fakeStore) DeleteSessionByTokenHash(_ context.Context, hash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range f.sessions {
		if s.TokenHash == hash {
			delete(f.sessions, id)
		}
	}
	return nil
}

func (f *fakeStore) DeleteSessionByID(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.sessions, id)
	return nil
}

func (f *fakeStore) DeleteUserSession(_ context.Context, userID, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.sessions[id]; !ok || s.UserID != userID {
		return domain.ErrNotFound
	}
	delete(f.sessions, id)
	return nil
}

func (f *fakeStore) DeleteOtherSessions(_ context.Context, userID, keepID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, s := range f.sessions {
		if s.UserID == userID && id != keepID {
			delete(f.sessions, id)
		}
	}
	return nil
}

func (f *fakeStore) DeleteExpiredSessions(_ context.Context, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, s := range f.sessions {
		if !s.ExpiresAt.After(now) {
			delete(f.sessions, id)
			n++
		}
	}
	return n, nil
}

// --- verifications ---

func (f *fakeStore) ReplaceVerification(_ context.Context, v domain.Verification) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, e := range f.verifs {
		if e.Identifier == v.Identifier {
			delete(f.verifs, id)
		}
	}
	f.verifs[v.ID] = v
	return nil
}

func (f *fakeStore) ConsumeVerification(_ context.Context, purpose, valueHash string, now time.Time) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for id, v := range f.verifs {
		if v.Value == valueHash && strings.HasPrefix(v.Identifier, purpose+":") && v.ExpiresAt.After(now) {
			delete(f.verifs, id)
			return v.Identifier, nil
		}
	}
	return "", domain.ErrNotFound
}

func (f *fakeStore) DeleteExpiredVerifications(_ context.Context, now time.Time) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var n int64
	for id, v := range f.verifs {
		if !v.ExpiresAt.After(now) {
			delete(f.verifs, id)
			n++
		}
	}
	return n, nil
}
