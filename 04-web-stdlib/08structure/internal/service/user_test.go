package service

import (
	"context"
	"errors"
	"testing"
	"time"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/domain"
)

// fakeStore records what the service stores and can be told to fail.
type fakeStore struct {
	created []domain.User
	err     error
}

func (f *fakeStore) Create(_ context.Context, u domain.User) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, u)
	return nil
}
func (f *fakeStore) Get(context.Context, uuid.UUID) (domain.User, error) { return domain.User{}, f.err }
func (f *fakeStore) List(context.Context) ([]domain.User, error)         { return f.created, f.err }

func TestRegisterNormalisesAndStores(t *testing.T) {
	st := &fakeStore{}
	svc := NewUsers(st)
	frozen := time.Date(2026, 5, 6, 7, 8, 9, 0, time.UTC)
	svc.now = func() time.Time { return frozen }

	u, err := svc.Register(t.Context(), RegisterInput{Email: "  Ada@Example.COM ", Name: "  Ada  "})
	if err != nil {
		t.Fatal(err)
	}
	if u.Email != "ada@example.com" || u.Name != "Ada" || !u.CreatedAt.Equal(frozen) || u.ID == uuid.Nil() {
		t.Errorf("unexpected user: %+v", u)
	}
	if len(st.created) != 1 || st.created[0] != u {
		t.Errorf("store got %+v", st.created)
	}
}

func TestRegisterValidation(t *testing.T) {
	tests := []struct {
		name       string
		in         RegisterInput
		wantFields []string
	}{
		{"empty", RegisterInput{}, []string{"email", "name"}},
		{"bad email", RegisterInput{Email: "nope", Name: "A"}, []string{"email"}},
		{"display name rejected", RegisterInput{Email: "Ann <ann@example.com>", Name: "A"}, []string{"email"}},
		{"long name", RegisterInput{Email: "a@b.co", Name: string(make([]rune, 81))}, []string{"name"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			st := &fakeStore{}
			_, err := NewUsers(st).Register(t.Context(), tt.in)

			ve, ok := errors.AsType[*domain.ValidationError](err)
			if !ok {
				t.Fatalf("err = %v, want *ValidationError", err)
			}
			for _, f := range tt.wantFields {
				if ve.Fields[f] == "" {
					t.Errorf("missing field error for %q: %v", f, ve.Fields)
				}
			}
			if len(ve.Fields) != len(tt.wantFields) {
				t.Errorf("fields = %v, want exactly %v", ve.Fields, tt.wantFields)
			}
			if len(st.created) != 0 {
				t.Error("invalid input must not reach the store")
			}
		})
	}
}

func TestStoreErrorsKeepTheirIdentity(t *testing.T) {
	st := &fakeStore{err: domain.ErrEmailTaken}
	_, err := NewUsers(st).Register(t.Context(), RegisterInput{Email: "a@b.co", Name: "A"})
	if !errors.Is(err, domain.ErrEmailTaken) {
		t.Errorf("err = %v; wrapping must preserve errors.Is", err)
	}
}
