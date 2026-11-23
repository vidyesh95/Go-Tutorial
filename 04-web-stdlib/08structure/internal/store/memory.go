// Package store is the persistence layer. This one keeps users in memory; the real thing
// would use database/sql (see 03-sql-sqlite) with the SAME method set, and nothing above
// it would change. That swap is the payoff of the layering.
//
// Note what is NOT here: no interface declaration. The store just exposes concrete methods.
// The service package declares the (small) interface it needs, and *MemoryUsers satisfies it
// implicitly. "Accept interfaces, return structs", and define the interface at the consumer.
package store

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/domain"
)

type MemoryUsers struct {
	mu      sync.RWMutex
	byID    map[uuid.UUID]domain.User
	byEmail map[string]uuid.UUID // stands in for a UNIQUE index in a real database
}

func NewMemoryUsers() *MemoryUsers {
	return &MemoryUsers{
		byID:    make(map[uuid.UUID]domain.User),
		byEmail: make(map[string]uuid.UUID),
	}
}

// ctx is unused by a map but is part of the signature because every real store (SQL, Redis,
// HTTP) needs it for cancellation and deadlines. Adding it later would change every caller.

func (s *MemoryUsers) Create(ctx context.Context, u domain.User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := strings.ToLower(u.Email)
	if _, taken := s.byEmail[key]; taken {
		return fmt.Errorf("create user %q: %w", u.Email, domain.ErrEmailTaken)
	}
	s.byID[u.ID] = u
	s.byEmail[key] = u.ID
	return nil
}

func (s *MemoryUsers) Get(ctx context.Context, id uuid.UUID) (domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	u, ok := s.byID[id]
	if !ok {
		return domain.User{}, fmt.Errorf("get user %s: %w", id, domain.ErrNotFound)
	}
	return u, nil
}

func (s *MemoryUsers) List(ctx context.Context) ([]domain.User, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]domain.User, 0, len(s.byID))
	for _, u := range s.byID {
		out = append(out, u)
	}
	slices.SortFunc(out, func(a, b domain.User) int { return a.ID.Compare(b.ID) }) // UUIDv7: creation order
	return out, nil
}
