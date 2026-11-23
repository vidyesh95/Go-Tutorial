// Lesson 28: Testing
//
// This folder has a library package (calc/calc.go) and its tests (calc/calc_test.go).
// This main.go simply uses the library; the real lesson is in calc/calc_test.go.
//
// Run the demo:
//
//	go run ./01-fundamentals/28testing
//
// Run the tests (from the repo root):
//
//	go test ./01-fundamentals/28testing/...                   # run all tests
//	go test -v ./01-fundamentals/28testing/...                # verbose: show each test and subtest
//	go test -run 'TestEval/add' ./01-fundamentals/28testing/... # only tests matching a regexp (subtests separated by /)
//	go test -race ./01-fundamentals/28testing/...             # enable the data race detector
//	go test -cover ./01-fundamentals/28testing/...            # print statement coverage
//	go test -short ./01-fundamentals/28testing/...            # skip tests that call testing.Short() checks
//	go test -bench=. -benchmem ./01-fundamentals/28testing/calc # run benchmarks (tests also run; add -run=^$ to skip them)
//	go test -fuzz=FuzzEval -fuzztime=5s ./01-fundamentals/28testing/calc # fuzz for 5 seconds (one package at a time)
//
// Testing rules of thumb:
//   - Test files end in _test.go and are never part of a normal build.
//   - Test functions: func TestXxx(t *testing.T). Examples: ExampleXxx(). Fuzz: FuzzXxx(f *testing.F).
//     Benchmarks: BenchmarkXxx(b *testing.B).
//   - t.Errorf marks failure and continues; t.Fatalf marks failure and stops this test.
//   - Table-driven tests with t.Run subtests are the Go standard style.
//   - Print "got X, want Y" in failures (in that order, with inputs) so failures are self-explanatory.
//   - No assertion library is needed. Compare with cmp.Diff (go-cmp) or slices.Equal/reflect.DeepEqual if needed.
//
// In industry: CI runs `go test -race -cover ./...` on every pull request. Teams use
// table-driven tests, fakes for dependencies behind small interfaces, httptest for
// handlers, fuzzing for parsers, and benchmarks (compared with benchstat) before
// performance-sensitive changes.
package main

import (
	"errors"
	"fmt"

	"github.com/vidyesh95/Go-Tutorial/01-fundamentals/28testing/calc"
)

func main() {
	fmt.Println("Add(2, 3) =", calc.Add(2, 3))

	v, err := calc.Eval("10 / 4")
	fmt.Println("Eval(\"10 / 4\") =", v, err)

	_, err = calc.Eval("1 / 0")
	fmt.Println("Eval(\"1 / 0\") error:", err, "| is ErrDivideByZero:", errors.Is(err, calc.ErrDivideByZero))

	fmt.Println("Now run: go test -v ./01-fundamentals/28testing/...")
}
