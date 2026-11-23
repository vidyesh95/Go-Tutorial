// Lesson 20: Errors
//
// In Go an error is just a value: anything with `Error() string`. Functions
// that can fail return it as the LAST result, and callers check it explicitly:
//
//	f, err := os.Open(name)
//	if err != nil {
//	    return fmt.Errorf("open config: %w", err)
//	}
//
// Tools:
//   - errors.New("msg")            create a simple error
//   - fmt.Errorf("ctx: %w", err)   add context AND keep the original for inspection
//   - sentinel errors              package-level vars (var ErrNotFound = errors.New(...))
//   - custom error types           structs with an Error() method and extra fields
//   - errors.Is(err, target)       "is target anywhere in the chain?" (compares values)
//   - errors.As(err, &target)      "is there a T in the chain?" (finds by type)
//   - errors.AsType[T](err)        Go 1.26 generic version of As: no target variable
//   - errors.Join(errs...)         combine several errors into one
//
// Key rules:
//   - Check every error. Ignoring one (`_ = f()`) should be a conscious decision.
//   - Wrap with %w (not %v) when callers may need to inspect the cause. %v
//     keeps only the text and breaks errors.Is/As.
//   - Add context at each layer ("load user 7: query: connection refused") but
//     do not repeat the word "failed"/"error" in every layer.
//   - Compare with errors.Is, NOT `err == ErrX`, because wrapping changes the value.
//   - Error strings are lower-case, without trailing punctuation. They are
//     usually embedded in a longer message.
//   - Do not panic for ordinary failures (bad input, missing file, network
//     down). Return an error. Panic is for programmer bugs and impossible
//     states (lesson 21).
//   - Handle an error ONCE: either return it or log it, not both, otherwise
//     the same failure is logged at every layer.
//
// Gotcha: returning a nil pointer of a concrete error type as `error` makes the
// interface non-nil (see lesson 17). Return a literal nil.
//
// In industry: repositories return sentinel or typed errors (ErrNotFound), the
// service layer wraps them with context, and the HTTP layer maps them to status
// codes with errors.Is/As (404, 409, 422) and logs unexpected ones with slog.
// Public API errors that carry codes use custom error types.
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// Sentinel errors: part of the package's API, compared with errors.Is.
var (
	ErrNotFound   = errors.New("not found")
	ErrPermission = errors.New("permission denied")
)

// ValidationError is a custom error type that carries structured data.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return "invalid " + e.Field + ": " + e.Reason
}

// --- a tiny three-layer app: repository -> service -> handler --------------

var users = map[int]string{1: "ada", 2: "grace"}

// repoFind is the lowest layer. It returns the sentinel error.
func repoFind(id int) (string, error) {
	name, ok := users[id]
	if !ok {
		return "", ErrNotFound
	}
	return name, nil
}

// serviceGet adds context for its layer, wrapping with %w.
func serviceGet(id int) (string, error) {
	if id <= 0 {
		return "", &ValidationError{Field: "id", Reason: "must be positive"}
	}
	name, err := repoFind(id)
	if err != nil {
		return "", fmt.Errorf("get user %d: %w", id, err)
	}
	return name, nil
}

// handle is the top layer: it decides what each kind of error MEANS.
func handle(id int) {
	name, err := serviceGet(id)

	var ve *ValidationError
	switch {
	case err == nil:
		fmt.Printf("  id=%d -> 200 OK %s\n", id, name)
	case errors.Is(err, ErrNotFound):
		fmt.Printf("  id=%d -> 404 (%v)\n", id, err)
	case errors.As(err, &ve):
		fmt.Printf("  id=%d -> 422 field=%s (%v)\n", id, ve.Field, err)
	default:
		fmt.Printf("  id=%d -> 500 (%v)\n", id, err)
	}
}

func main() {
	fmt.Println("Errors in Go")

	// Basic: create, return, check.
	_, err := strconv.Atoi("abc")
	fmt.Println("Atoi error:", err)

	// Context chain across layers.
	fmt.Println("Handling requests:")
	handle(1)
	handle(99)
	handle(-1)

	// Wrapping with %w keeps the chain, %v breaks it.
	base := ErrPermission
	wrapped := fmt.Errorf("delete file: %w", base)
	flat := fmt.Errorf("delete file: %v", base)
	fmt.Println("Is(wrapped, ErrPermission):", errors.Is(wrapped, ErrPermission))
	fmt.Println("Is(flat, ErrPermission):", errors.Is(flat, ErrPermission), "<- %v lost the cause")
	fmt.Println("Unwrap:", errors.Unwrap(wrapped))

	// errors.As: find a typed error anywhere in the chain.
	chain := fmt.Errorf("signup: %w", &ValidationError{Field: "email", Reason: "missing @"})
	var ve *ValidationError
	if errors.As(chain, &ve) {
		fmt.Println("As found field:", ve.Field)
	}

	// errors.AsType (Go 1.26): returns the value and a bool, no target variable.
	if ve, ok := errors.AsType[*ValidationError](chain); ok {
		fmt.Println("AsType found reason:", ve.Reason)
	}

	// Standard library errors work the same way.
	_, err = os.Open("/definitely/not/here.txt")
	fmt.Println("os.Open error is ErrNotExist:", errors.Is(err, os.ErrNotExist))
	if pe, ok := errors.AsType[*os.PathError](err); ok {
		fmt.Println("PathError op:", pe.Op, "path:", pe.Path)
	}

	// errors.Join combines independent errors (e.g. validating many fields).
	joined := errors.Join(
		&ValidationError{Field: "name", Reason: "empty"},
		&ValidationError{Field: "age", Reason: "negative"},
	)
	fmt.Println("Joined error:")
	fmt.Println(joined)
	fmt.Println("Joined still matches As:", errors.As(joined, &ve), "| first:", ve.Field)

	// Join drops nil values; if all are nil it returns nil.
	fmt.Println("Join of nils is nil:", errors.Join(nil, nil) == nil)

	// The typed-nil trap, one more time.
	fmt.Println("nilCheck:", mightFail(false) == nil)
}

// mightFail returns a literal nil on success, never a typed nil pointer.
func mightFail(fail bool) error {
	if fail {
		return &ValidationError{Field: "x", Reason: "bad"}
	}
	return nil
}
