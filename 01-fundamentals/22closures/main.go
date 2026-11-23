// Lesson 22: Closures, function values and functional options
//
// Functions are FIRST-CLASS values in Go: you can store them in variables,
// pass them as arguments, return them, and give their types names.
//
//	var f func(int) int = double
//	type Handler func(string) error
//
// A CLOSURE is a function literal that captures variables from the scope where
// it was created. It keeps access to them even after that scope has returned;
// the captured variables live on the heap.
//
// Key rules:
//   - A closure captures VARIABLES, not values. If the variable changes, the
//     closure sees the new value.
//   - Since Go 1.22 each loop iteration has its own copy of the loop variable,
//     so closures created in a loop capture a distinct variable each time.
//   - Variadic parameters: `func sum(nums ...int)`. Inside, nums is a []int.
//     Pass an existing slice with `sum(xs...)`.
//   - A variadic parameter must be last. Calling with no values gives a nil slice.
//   - A function type can have methods. http.HandlerFunc is the classic
//     example: it makes a plain function satisfy the http.Handler interface.
//   - Function values are not comparable (except to nil).
//
// Gotcha: closures that mutate shared captured state are not safe to call from
// several goroutines without synchronisation.
//
// In industry:
//   - Higher-order functions (map/filter helpers, slices.SortFunc, retry wrappers)
//     and middleware (func(http.Handler) http.Handler) are everywhere.
//   - Function types as strategies replace one-method interfaces (e.g. a
//     `type Hasher func([]byte) string`).
//   - The FUNCTIONAL OPTIONS pattern gives constructors optional, readable,
//     backwards-compatible configuration:
//     NewServer(addr, WithTimeout(5*time.Second), WithLogger(l)).
//     You will see it in gRPC, Zap, and many internal libraries.
package main

import (
	"cmp"
	"fmt"
	"slices"
	"strings"
	"time"
)

type person struct {
	Name string
	Age  int
}

// --- function values and function types -----------------------------------

func double(n int) int { return n * 2 }
func square(n int) int { return n * n }

// Transformer is a named function type. It documents intent and can have methods.
type Transformer func(int) int

// Then composes two transformers: first t, then next.
func (t Transformer) Then(next Transformer) Transformer {
	return func(n int) int { return next(t(n)) }
}

// apply is a higher-order function: it takes a function as a parameter.
func apply(nums []int, f func(int) int) []int {
	out := make([]int, 0, len(nums))
	for _, n := range nums {
		out = append(out, f(n))
	}
	return out
}

// filter keeps the elements for which keep returns true.
func filter(nums []int, keep func(int) bool) []int {
	var out []int
	for _, n := range nums {
		if keep(n) {
			out = append(out, n)
		}
	}
	return out
}

// --- closures -------------------------------------------------------------

// counter returns a function that remembers its own private count.
func counter() func() int {
	count := 0 // captured variable
	return func() int {
		count++
		return count
	}
}

// multiplier returns a function that captures the factor.
func multiplier(factor int) func(int) int {
	return func(n int) int { return n * factor }
}

// --- variadic -------------------------------------------------------------

func sum(label string, nums ...int) string {
	total := 0
	for _, n := range nums {
		total += n
	}
	return fmt.Sprintf("%s: %d numbers, total %d", label, len(nums), total)
}

// --- strategy via function type ------------------------------------------

// Normalizer is a strategy: any func(string) string.
type Normalizer func(string) string

func process(inputs []string, norm Normalizer) []string {
	out := make([]string, len(inputs))
	for i, s := range inputs {
		out[i] = norm(s)
	}
	return out
}

// --- functional options ---------------------------------------------------

// Server has a few required and several optional settings.
type Server struct {
	addr     string
	timeout  time.Duration
	maxConns int
	logger   func(string)
}

// Option is a function that configures a Server.
type Option func(*Server)

func WithTimeout(d time.Duration) Option {
	return func(s *Server) { s.timeout = d }
}

func WithMaxConns(n int) Option {
	return func(s *Server) { s.maxConns = n }
}

func WithLogger(l func(string)) Option {
	return func(s *Server) { s.logger = l }
}

// NewServer takes required values as normal parameters and the rest as options.
// Defaults are set first, then every option is applied.
func NewServer(addr string, opts ...Option) *Server {
	s := &Server{
		addr:     addr,
		timeout:  30 * time.Second,
		maxConns: 100,
		logger:   func(string) {}, // safe no-op default
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

func (s *Server) String() string {
	return fmt.Sprintf("Server{addr=%s timeout=%s maxConns=%d}", s.addr, s.timeout, s.maxConns)
}

func main() {
	fmt.Println("Closures and function values")

	// Function values.
	f := double
	fmt.Println("f(4):", f(4))

	nums := []int{1, 2, 3, 4, 5}
	fmt.Println("apply double:", apply(nums, double))
	fmt.Println("apply square:", apply(nums, square))

	// Anonymous function literal, passed inline.
	fmt.Println("apply +100:", apply(nums, func(n int) int { return n + 100 }))
	fmt.Println("filter even:", filter(nums, func(n int) bool { return n%2 == 0 }))

	// Method on a function type.
	both := Transformer(double).Then(square)
	fmt.Println("double then square of 3:", both(3))

	// Immediately-invoked function literal.
	result := func(a, b int) int { return a * b }(6, 7)
	fmt.Println("IIFE:", result)

	// Closures keep their own state.
	next := counter()
	next()
	next()
	fmt.Println("counter A:", next())
	other := counter() // independent state
	fmt.Println("counter B:", other())

	triple := multiplier(3)
	fmt.Println("multiplier(3)(5):", triple(5))

	// The closure captures the VARIABLE, so later changes are visible.
	x := 10
	show := func() int { return x }
	x = 99
	fmt.Println("closure sees updated x:", show())

	// Per-iteration loop variables: each closure gets its own i.
	var fns []func() int
	for i := range 3 {
		fns = append(fns, func() int { return i * i })
	}
	for _, fn := range fns {
		fmt.Print(fn(), " ")
	}
	fmt.Println("<- squares of 0, 1, 2")

	// Variadic functions.
	fmt.Println(sum("none"))
	fmt.Println(sum("three", 1, 2, 3))
	xs := []int{10, 20}
	fmt.Println(sum("spread", xs...)) // pass a slice with ...

	// Strategy: swap behaviour by passing a function.
	words := []string{"  Go ", " RUST", "zig  "}
	fmt.Printf("trim+lower: %q\n", process(words, func(s string) string {
		return strings.ToLower(strings.TrimSpace(s))
	}))
	fmt.Printf("upper only: %q\n", process(words, strings.ToUpper))

	// Standard library higher-order function: slices.SortFunc takes a comparison.
	people := []person{{"Ann", 31}, {"Bob", 25}, {"Cy", 40}}
	slices.SortFunc(people, func(a, b person) int {
		return cmp.Compare(a.Age, b.Age) // negative, zero or positive
	})
	fmt.Println("sorted by age:", people)

	// Functional options.
	def := NewServer(":8080")
	fmt.Println("defaults:", def)

	var logs []string
	custom := NewServer(":9090",
		WithTimeout(5*time.Second),
		WithMaxConns(10),
		WithLogger(func(m string) { logs = append(logs, m) }),
	)
	custom.logger("started")
	fmt.Println("custom:", custom, "| captured logs:", logs)
}
