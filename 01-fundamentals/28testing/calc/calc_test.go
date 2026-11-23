package calc

// Tests for package calc. Because this file is in package calc (not calc_test) it can
// also see unexported identifiers. Use `package calc_test` to test only the public API.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
)

// TestAdd is the simplest form: one case, plain if + t.Errorf.
func TestAdd(t *testing.T) {
	if got, want := Add(2, 3), 5; got != want {
		t.Errorf("Add(2, 3) = %d, want %d", got, want)
	}
}

// TestEval is a TABLE-DRIVEN test: a slice of cases and one loop. Adding a case is one line.
// Each case runs as a subtest via t.Run, so failures name the case and you can run one
// with: go test -run 'TestEval/add'
func TestEval(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		want    float64
		wantErr error // nil means success expected; use errors.Is on sentinel errors
		anyErr  bool  // true when we only care that SOME error happens
	}{
		{name: "add", expr: "3 + 4", want: 7},
		{name: "subtract", expr: "3 - 4", want: -1},
		{name: "multiply", expr: "2.5 * 4", want: 10},
		{name: "divide", expr: "9 / 3", want: 3},
		{name: "divide by zero", expr: "1 / 0", wantErr: ErrDivideByZero},
		{name: "too few fields", expr: "1 +", anyErr: true},
		{name: "bad number", expr: "x + 1", anyErr: true},
		{name: "bad operator", expr: "1 ^ 2", anyErr: true},
	}
	for _, tt := range tests {
		// Go 1.22+: tt is a fresh variable per iteration, so no `tt := tt` copy is needed
		// even with t.Parallel below.
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // subtests with Parallel run concurrently; they must not share mutable state

			got, err := Eval(tt.expr)
			switch {
			case tt.wantErr != nil:
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Eval(%q) error = %v, want %v", tt.expr, err, tt.wantErr)
				}
			case tt.anyErr:
				if err == nil {
					t.Fatalf("Eval(%q) expected an error, got %v", tt.expr, got)
				}
			default:
				if err != nil {
					t.Fatalf("Eval(%q) unexpected error: %v", tt.expr, err)
				}
				if got != tt.want {
					t.Errorf("Eval(%q) = %v, want %v", tt.expr, got, tt.want)
				}
			}
		})
	}
}

// assertEqualFloat is a HELPER: t.Helper() makes failures report the line in the TEST that
// called the helper, not the line inside the helper.
func assertEqualFloat(t *testing.T, got, want float64) {
	t.Helper()
	if got != want {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestDivideWithHelper(t *testing.T) {
	got, err := Divide(10, 4)
	if err != nil {
		t.Fatal(err) // Fatal stops this test immediately
	}
	assertEqualFloat(t, got, 2.5)
}

// TestSaveLoad shows t.TempDir (a fresh directory, deleted automatically after the test)
// and t.Cleanup (runs registered functions in LIFO order when the test ends).
func TestSaveLoad(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "result.txt")

	t.Cleanup(func() {
		// Real tests use this to close DB connections, stop servers, reset globals...
		t.Log("cleanup runs after the test (and its subtests) finish")
	})

	if err := SaveResult(path, 3.25); err != nil {
		t.Fatalf("SaveResult: %v", err)
	}
	got, err := LoadResult(path)
	if err != nil {
		t.Fatalf("LoadResult: %v", err)
	}
	assertEqualFloat(t, got, 3.25)
}

// TestSumSlowCancel shows t.Context (Go 1.24): a context that is cancelled just before
// Cleanup functions run, perfect for stopping goroutines a test started.
func TestSumSlowCancel(t *testing.T) {
	got, err := SumSlow(t.Context(), []int{1, 2, 3})
	if err != nil || got != 6 {
		t.Fatalf("SumSlow = %d, %v; want 6, nil", got, err)
	}

	// An already-cancelled context must make the function return the context's error.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := SumSlow(ctx, []int{1}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// TestBigSum is "slow": skip it with `go test -short`. Use this for integration-style tests
// that need a database, network or lots of time.
func TestBigSum(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping slow test in -short mode")
	}
	nums := make([]int, 1_000_000)
	for i := range nums {
		nums[i] = 1
	}
	got, err := SumSlow(t.Context(), nums)
	if err != nil || got != 1_000_000 {
		t.Fatalf("got %d, %v", got, err)
	}
}

// ExampleEval is an EXAMPLE test: it is compiled, run, and its stdout is compared with the
// "// Output:" comment. It also appears as documentation in `go doc` and pkg.go.dev.
func ExampleEval() {
	v, _ := Eval("6 * 7")
	fmt.Println(v)
	// Output: 42
}

// FuzzEval is a FUZZ test. `go test` runs the seed corpus (f.Add) as normal tests;
// `go test -fuzz=FuzzEval -fuzztime=5s` generates random inputs looking for panics or
// broken invariants. Failing inputs are saved under testdata/fuzz/ and replayed afterwards.
func FuzzEval(f *testing.F) {
	f.Add("1 + 2")
	f.Add("")
	f.Add("1 / 0")
	f.Add("nan * inf")
	f.Fuzz(func(t *testing.T, expr string) {
		// Property: Eval must never panic, whatever the input. If it returns an error,
		// the value must be zero (our documented contract).
		got, err := Eval(expr)
		if err != nil && got != 0 {
			t.Errorf("Eval(%q) returned %v with error %v", expr, got, err)
		}
	})
}

// BenchmarkEval measures speed. b.Loop() (Go 1.24) runs the body the right number of times,
// keeps the compiler from optimising the work away, and excludes setup before the loop.
// Run: go test -bench=Eval -benchmem ./01-fundamentals/28testing/calc
func BenchmarkEval(b *testing.B) {
	for b.Loop() {
		_, _ = Eval("12.5 * 3")
	}
}
