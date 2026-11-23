package store

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/domain"
)

func user(email string) domain.User {
	return domain.User{ID: uuid.NewV7(), Email: email, Name: "N", CreatedAt: time.Now()}
}

func TestCreateGetList(t *testing.T) {
	s := NewMemoryUsers()
	a, b := user("a@x.co"), user("b@x.co")
	for _, u := range []domain.User{a, b} {
		if err := s.Create(t.Context(), u); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Get(t.Context(), a.ID)
	if err != nil || got != a {
		t.Errorf("Get = %+v, %v", got, err)
	}
	list, _ := s.List(t.Context())
	if len(list) != 2 || list[0].ID != a.ID || list[1].ID != b.ID {
		t.Errorf("List order wrong: %+v", list)
	}
}

func TestErrors(t *testing.T) {
	s := NewMemoryUsers()
	s.Create(t.Context(), user("a@x.co"))

	if err := s.Create(t.Context(), user("A@X.CO")); !errors.Is(err, domain.ErrEmailTaken) {
		t.Errorf("duplicate (case-insensitive) = %v", err)
	}
	if _, err := s.Get(t.Context(), uuid.NewV7()); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing = %v", err)
	}
}

func TestConcurrentUse(t *testing.T) {
	s := NewMemoryUsers()
	var wg sync.WaitGroup
	for i := range 40 {
		wg.Go(func() {
			if err := s.Create(t.Context(), user(fmt.Sprintf("u%d@x.co", i))); err != nil {
				t.Error(err)
			}
			s.List(t.Context())
		})
	}
	wg.Wait()
	if list, _ := s.List(t.Context()); len(list) != 40 {
		t.Errorf("len = %d", len(list))
	}
}
