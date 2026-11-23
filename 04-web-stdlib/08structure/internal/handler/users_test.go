package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/service"
)

// fakeService lets each test decide exactly what the business layer returns. Function
// fields are the cheapest fake: only set what the test needs. No HTTP, no store, no mocks library.
type fakeService struct {
	register func(ctx context.Context, in service.RegisterInput) (domain.User, error)
	get      func(ctx context.Context, id uuid.UUID) (domain.User, error)
	list     func(ctx context.Context) ([]domain.User, error)
}

func (f fakeService) Register(ctx context.Context, in service.RegisterInput) (domain.User, error) {
	return f.register(ctx, in)
}
func (f fakeService) Get(ctx context.Context, id uuid.UUID) (domain.User, error) {
	return f.get(ctx, id)
}
func (f fakeService) List(ctx context.Context) ([]domain.User, error) { return f.list(ctx) }

func do(t *testing.T, svc UserService, method, target, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := New(svc, slog.New(slog.DiscardHandler)).Routes() // slog.DiscardHandler: Go 1.24+
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

var sampleUser = domain.User{
	ID:        uuid.MustParse("01a207fb-efff-7537-bb76-ef022a6a154b"),
	Email:     "ada@example.com",
	Name:      "Ada",
	CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC),
}

type errBody struct {
	Error struct {
		Code   string
		Fields map[string]string
	}
}

// The single mapping table: every domain error -> status + code.
func TestDomainErrorsMapToHTTP(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
	}{
		{"not found", domain.ErrNotFound, 404, "not_found"},
		{"wrapped not found", fmt.Errorf("get user: %w", domain.ErrNotFound), 404, "not_found"},
		{"email taken", fmt.Errorf("register: %w", domain.ErrEmailTaken), 409, "email_taken"},
		{"validation", &domain.ValidationError{Fields: map[string]string{"name": "required"}}, 422, "validation_failed"},
		{"unexpected", errors.New("connection refused to 10.0.0.5:5432"), 500, "internal"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := fakeService{get: func(context.Context, uuid.UUID) (domain.User, error) { return domain.User{}, tt.err }}
			rec := do(t, svc, "GET", "/users/"+sampleUser.ID.String(), "")

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			var body errBody
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Error.Code != tt.wantCode {
				t.Errorf("code = %q, want %q", body.Error.Code, tt.wantCode)
			}
			if tt.wantStatus == 500 && strings.Contains(rec.Body.String(), "10.0.0.5") {
				t.Error("internal error details leaked to the client")
			}
		})
	}
}

func TestValidationFieldsAreForwarded(t *testing.T) {
	svc := fakeService{register: func(context.Context, service.RegisterInput) (domain.User, error) {
		return domain.User{}, &domain.ValidationError{Fields: map[string]string{"email": "bad"}}
	}}
	rec := do(t, svc, "POST", "/users", `{"email":"x","name":"y"}`)
	var body errBody
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Error.Fields["email"] != "bad" {
		t.Errorf("fields = %v", body.Error.Fields)
	}
}

func TestRegisterSuccess(t *testing.T) {
	var got service.RegisterInput
	svc := fakeService{register: func(_ context.Context, in service.RegisterInput) (domain.User, error) {
		got = in
		return sampleUser, nil
	}}
	rec := do(t, svc, "POST", "/users", `{"email":"ada@example.com","name":"Ada"}`)

	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if got.Email != "ada@example.com" || got.Name != "Ada" {
		t.Errorf("service received %+v", got)
	}
	if loc := rec.Header().Get("Location"); loc != "/users/"+sampleUser.ID.String() {
		t.Errorf("Location = %q", loc)
	}
	var body userJSON
	json.Unmarshal(rec.Body.Bytes(), &body)
	if body.ID != sampleUser.ID.String() || body.Email != "ada@example.com" {
		t.Errorf("body = %+v", body)
	}
}

func TestBadRequests(t *testing.T) {
	// The fake has no functions set: calling it would panic, proving the handler rejects
	// these requests BEFORE reaching the service.
	svc := fakeService{}
	tests := []struct{ name, body string }{
		{"malformed", `{"email":`},
		{"unknown field", `{"email":"a@b.c","name":"x","admin":true}`},
		{"empty", ``},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if rec := do(t, svc, "POST", "/users", tt.body); rec.Code != 400 {
				t.Errorf("status = %d, want 400", rec.Code)
			}
		})
	}
}

func TestMalformedIDIsNotFound(t *testing.T) {
	if rec := do(t, fakeService{}, "GET", "/users/not-a-uuid", ""); rec.Code != 404 {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestListEmptyEncodesAsArray(t *testing.T) {
	svc := fakeService{list: func(context.Context) ([]domain.User, error) { return nil, nil }}
	rec := do(t, svc, "GET", "/users", "")
	if got := strings.TrimSpace(rec.Body.String()); got != `{"users":[]}` {
		t.Errorf("body = %s, want {\"users\":[]}", got)
	}
}

func TestContextReachesService(t *testing.T) {
	type key struct{}
	var seen bool
	svc := fakeService{list: func(ctx context.Context) ([]domain.User, error) {
		seen = ctx.Value(key{}) == "v"
		return nil, nil
	}}
	h := New(svc, slog.New(slog.DiscardHandler)).Routes()
	req := httptest.NewRequest("GET", "/users", nil)
	req = req.WithContext(context.WithValue(req.Context(), key{}, "v"))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if !seen {
		t.Error("request context was not passed to the service")
	}
}
