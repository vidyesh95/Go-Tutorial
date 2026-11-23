package main

import (
	"fmt"
	"testing"
)

// Correctness first: a benchmark of a function that returns the wrong answer is meaningless.
func TestImplementationsAgree(t *testing.T) {
	for _, n := range []int{0, 1, 10, 1000} {
		want := concat(n)
		for name, fn := range map[string]func(int) string{
			"builder": builder, "builderGrow": builderGrow, "builderAppend": builderAppend,
		} {
			if got := fn(n); got != want {
				t.Errorf("%s(%d) differs from concat", name, n)
			}
		}
	}
}

func TestEscapeAnalysisAllocations(t *testing.T) {
	if got := testing.AllocsPerRun(100, func() { _ = sumOnStack(1, 2) }); got != 0 {
		t.Errorf("sumOnStack allocates %v times, want 0", got)
	}
	if got := testing.AllocsPerRun(100, func() { sink = leakToHeap(1, 2) }); got < 1 {
		t.Errorf("leakToHeap should allocate, got %v", got)
	}
}

// Run:  go test -bench=. -benchmem ./08-use-cases/07pprof
//
// b.Loop (Go 1.24+) replaces the old `for i := 0; i < b.N; i++`. Advantages:
//   - setup before the loop and cleanup after it are NOT timed, automatically (no b.ResetTimer);
//   - the compiler is prevented from optimising away calls inside the loop body, so the
//     function is not deleted as "result unused";
//   - the benchmark function body runs once per measurement, not once per b.N batch.
//
// Reading the output:
//
//	BenchmarkBuild/concat/n=1000-8   2000   550000 ns/op   2900000 B/op   2000 allocs/op
//	                 name         -GOMAXPROCS  iterations   time per op    bytes per op   allocations per op
//
// With -benchmem, B/op and allocs/op show memory behaviour. A big difference there explains
// the time difference: every allocation costs time now and garbage collection work later.
func BenchmarkBuild(b *testing.B) {
	impls := []struct {
		name string
		fn   func(int) string
	}{
		{"concat", concat},
		{"builder", builder},
		{"builderGrow", builderGrow},
		{"builderAppend", builderAppend},
	}
	for _, n := range []int{100, 1_000, 10_000} {
		for _, impl := range impls {
			// Skip the quadratic one at the largest size so the benchmark run stays short.
			if impl.name == "concat" && n > 1_000 {
				continue
			}
			b.Run(fmt.Sprintf("%s/n=%d", impl.name, n), func(b *testing.B) {
				b.ReportAllocs() // same as passing -benchmem, but always on for this benchmark
				for b.Loop() {
					impl.fn(n)
				}
			})
		}
	}
}

// Benchmarks double as profiling harnesses:
//
//	go test -bench=Fib -cpuprofile cpu.out ./08-use-cases/07pprof && go tool pprof -top cpu.out
func BenchmarkFib(b *testing.B) {
	for b.Loop() {
		fib(25)
	}
}
