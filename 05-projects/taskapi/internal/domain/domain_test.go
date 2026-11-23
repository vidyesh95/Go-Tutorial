package domain

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestSignupInputValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		in         SignupInput
		wantFields []string // nil = valid
	}{
		{"valid", SignupInput{Email: "ada@example.com", Name: "Ada", Password: "correct horse"}, nil},
		{"bad email", SignupInput{Email: "nope", Name: "Ada", Password: "correct horse"}, []string{"email"}},
		{"display name rejected", SignupInput{Email: "Ada <ada@example.com>", Name: "Ada", Password: "correct horse"}, []string{"email"}},
		{"empty name", SignupInput{Email: "ada@example.com", Name: "", Password: "correct horse"}, []string{"name"}},
		{"short password", SignupInput{Email: "ada@example.com", Name: "Ada", Password: "short"}, []string{"password"}},
		{"password over the cap", SignupInput{Email: "ada@example.com", Name: "Ada", Password: strings.Repeat("x", MaxPasswordBytes+1)}, []string{"password"}},
		{"long password is fine now (argon2id has no 72 byte limit)", SignupInput{Email: "ada@example.com", Name: "Ada", Password: strings.Repeat("x", MaxPasswordBytes)}, nil},
		{"everything wrong", SignupInput{}, []string{"email", "name", "password"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertFields(t, tt.in.Validate(), tt.wantFields)
		})
	}
}

func TestSignupInputNormalize(t *testing.T) {
	in := SignupInput{Email: "  Ada@Example.COM ", Name: " Ada "}
	in.Normalize()
	if in.Email != "ada@example.com" || in.Name != "Ada" {
		t.Fatalf("got %+v", in)
	}
}

func TestCreateTaskInputValidate(t *testing.T) {
	t.Parallel()
	p := func(n int) *int { return &n }
	s := func(v string) *string { return &v }
	tests := []struct {
		name       string
		in         CreateTaskInput
		wantFields []string
	}{
		{"title only", CreateTaskInput{Title: "Write docs"}, nil},
		{"all fields", CreateTaskInput{Title: "x", Priority: p(5), DueDate: s("2026-12-31")}, nil},
		{"blank title", CreateTaskInput{Title: "   "}, []string{"title"}},
		{"title too long", CreateTaskInput{Title: strings.Repeat("a", 201)}, []string{"title"}},
		{"priority low", CreateTaskInput{Title: "x", Priority: p(0)}, []string{"priority"}},
		{"priority high", CreateTaskInput{Title: "x", Priority: p(6)}, []string{"priority"}},
		{"impossible date", CreateTaskInput{Title: "x", DueDate: s("2026-02-30")}, []string{"due_date"}},
		{"wrong date format", CreateTaskInput{Title: "x", DueDate: s("31/12/2026")}, []string{"due_date"}},
		{"empty date not allowed on create", CreateTaskInput{Title: "x", DueDate: s("")}, []string{"due_date"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assertFields(t, tt.in.Validate(), tt.wantFields)
		})
	}
}

func TestUpdateTaskInput(t *testing.T) {
	t.Parallel()
	s := func(v string) *string { return &v }
	st := func(v TaskStatus) *TaskStatus { return &v }

	assertFields(t, UpdateTaskInput{}.Validate(), []string{"body"})
	assertFields(t, UpdateTaskInput{Status: st("bogus")}.Validate(), []string{"status"})
	assertFields(t, UpdateTaskInput{Status: st(StatusDone)}.Validate(), nil)
	assertFields(t, UpdateTaskInput{DueDate: s("")}.Validate(), nil) // "" clears

	due := "2026-01-01"
	task := Task{Title: "old", Status: StatusTodo, Priority: 3, DueDate: &due}
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	UpdateTaskInput{Title: s("new"), Status: st(StatusDoing), DueDate: s("")}.Apply(&task, now)
	if task.Title != "new" || task.Status != StatusDoing || task.Priority != 3 || task.DueDate != nil || !task.UpdatedAt.Equal(now) {
		t.Fatalf("unexpected task after Apply: %+v", task)
	}
}

func TestNormalizeLimit(t *testing.T) {
	for in, want := range map[int]int{-5: 20, 0: 20, 1: 1, 100: 100, 101: 100, 5000: 100} {
		if got := NormalizeLimit(in); got != want {
			t.Errorf("NormalizeLimit(%d) = %d, want %d", in, got, want)
		}
	}
}

func TestValidationErrorMessageIsSorted(t *testing.T) {
	err := &ValidationError{Fields: map[string]string{"b": "2", "a": "1"}}
	if got, want := err.Error(), "validation failed: a: 1; b: 2"; got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

// assertFields checks that err is nil (want == nil) or a *ValidationError naming exactly want.
func assertFields(t *testing.T, err error, want []string) {
	t.Helper()
	if want == nil {
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		return
	}
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if len(ve.Fields) != len(want) {
		t.Fatalf("fields = %v, want exactly %v", ve.Fields, want)
	}
	for _, name := range want {
		if _, ok := ve.Fields[name]; !ok {
			t.Fatalf("fields = %v, missing %q", ve.Fields, name)
		}
	}
}
