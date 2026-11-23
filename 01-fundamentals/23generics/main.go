// Lesson 23: Generics (type parameters)
//
// Generics (Go 1.18+) let you write one function or type that works for many
// types, while the compiler still checks types at build time.
//
//	func Map[T, U any](xs []T, f func(T) U) []U
//	         ^^^^^^^^^^ type parameter list, in square brackets
//
// Key ideas:
//   - A type parameter has a CONSTRAINT: an interface describing which types are allowed.
//   - `any` allows every type (but then you can only do things every type can do).
//   - `comparable` allows types usable with == and != (needed for map keys).
//   - `cmp.Ordered` allows types usable with < <= > >= (ints, floats, strings).
//   - A custom constraint can be a union of types: `~int | ~float64`.
//     The `~` means "any type whose UNDERLYING type is int", so `type Celsius float64`
//     is accepted too. Without `~` only the exact named type is allowed.
//   - Type arguments are usually inferred: Map(nums, strconv.Itoa), no need to write Map[int,string].
//   - Generic types (Stack[T]) are instantiated explicitly: Stack[int]{}.
//   - Methods cannot have their own type parameters; only the receiver's.
//   - Since Go 1.24, type aliases may be generic too: type Pair[K comparable, V any] = ...
//
// When NOT to use generics:
//   - When an interface already says it: if you only call methods, accept an interface
//     (io.Reader, fmt.Stringer), not [T io.Reader].
//   - When there is only one concrete type today. Duplicate a little code first;
//     abstract when the second or third use shows the real shape.
//   - When the function body needs a type switch on T: that is just an interface in disguise.
//   - Do not make code harder to read for a tiny reuse win.
//
// In industry: generics shine in small, boring utilities: slices/maps helpers,
// typed caches and sets, result/optional wrappers, repository helpers like
// Paginate[T], and worker pools of Job[T]. Go teams avoid heavy generic
// frameworks; the standard library's slices, maps and cmp packages are the
// style guide: small, concrete, easy to read.
package main

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
)

// ---- Generic functions -----------------------------------------------------

// Map applies f to every element. T and U are inferred from the arguments.
func Map[T, U any](xs []T, f func(T) U) []U {
	out := make([]U, 0, len(xs))
	for _, x := range xs {
		out = append(out, f(x))
	}
	return out
}

// Filter keeps the elements for which keep returns true.
func Filter[T any](xs []T, keep func(T) bool) []T {
	var out []T
	for _, x := range xs {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

// Reduce folds a slice into one value, starting from init.
func Reduce[T, A any](xs []T, init A, f func(A, T) A) A {
	acc := init
	for _, x := range xs {
		acc = f(acc, x)
	}
	return acc
}

// MaxOf works for any ordered type thanks to cmp.Ordered.
// (The builtin max already does this for a few args; this shows a slice version.)
func MaxOf[T cmp.Ordered](xs ...T) (T, bool) {
	var zero T
	if len(xs) == 0 {
		return zero, false
	}
	m := xs[0]
	for _, x := range xs[1:] {
		m = max(m, x)
	}
	return m, true
}

// Index returns the position of target, or -1. It needs `comparable` for ==.
func Index[T comparable](xs []T, target T) int {
	for i, x := range xs {
		if x == target {
			return i
		}
	}
	return -1
}

// ---- Custom constraint with a type union and ~ ----------------------------

// Number is a custom constraint: any type whose underlying type is one of these.
type Number interface {
	~int | ~int64 | ~float64
}

// Sum adds up numbers of any Number type.
func Sum[T Number](xs []T) T {
	var total T
	for _, x := range xs {
		total += x
	}
	return total
}

// Celsius has underlying type float64; the ~ in Number lets it through.
type Celsius float64

// ---- Generic types ---------------------------------------------------------

// Stack is a LIFO stack of any element type.
type Stack[T any] struct {
	items []T
}

// Push adds an item on top.
func (s *Stack[T]) Push(v T) { s.items = append(s.items, v) }

// Pop removes and returns the top item. ok is false when the stack is empty.
func (s *Stack[T]) Pop() (v T, ok bool) {
	if len(s.items) == 0 {
		return v, false // v is the zero value of T
	}
	last := len(s.items) - 1
	v = s.items[last]
	s.items = s.items[:last]
	return v, true
}

// Set is a set of unique comparable values. Map keys must be comparable,
// so the constraint is required, not optional.
type Set[T comparable] struct {
	m map[T]struct{} // struct{} uses zero bytes per value
}

// NewSet builds a set from the given values.
func NewSet[T comparable](vals ...T) *Set[T] {
	s := &Set[T]{m: make(map[T]struct{}, len(vals))}
	for _, v := range vals {
		s.Add(v)
	}
	return s
}

func (s *Set[T]) Add(v T)      { s.m[v] = struct{}{} }
func (s *Set[T]) Has(v T) bool { _, ok := s.m[v]; return ok }
func (s *Set[T]) Len() int     { return len(s.m) }

// Generic type alias (Go 1.24): a shorter name for a generic type.
type Pair[K comparable, V any] struct {
	Key K
	Val V
}

// StringPairs is a generic alias: a partially-applied Pair with string keys.
type StringPairs[V any] = []Pair[string, V]

func main() {
	nums := []int{1, 2, 3, 4, 5, 6}

	fmt.Println("--- Map / Filter / Reduce ---")
	strs := Map(nums, strconv.Itoa) // T=int, U=string inferred
	fmt.Printf("Map to strings: %q\n", strs)
	evens := Filter(nums, func(n int) bool { return n%2 == 0 })
	fmt.Println("Filter evens:  ", evens)
	total := Reduce(nums, 0, func(acc, n int) int { return acc + n })
	fmt.Println("Reduce sum:    ", total)

	fmt.Println("\n--- Constraints ---")
	m, _ := MaxOf("pear", "apple", "zebra") // strings are cmp.Ordered
	fmt.Println("MaxOf strings:", m)
	fmt.Println("Index of 3:   ", Index(nums, 3))
	fmt.Println("Sum ints:     ", Sum(nums))
	temps := []Celsius{20.5, 22, 19.5}
	fmt.Println("Sum Celsius:  ", Sum(temps)) // works because of ~float64

	fmt.Println("\n--- Generic types ---")
	var st Stack[string] // explicit instantiation
	st.Push("a")
	st.Push("b")
	top, _ := st.Pop()
	fmt.Println("Popped:", top)
	_, ok := (&Stack[int]{}).Pop()
	fmt.Println("Pop on empty stack ok?", ok)

	set := NewSet("go", "rust", "go")
	fmt.Println("Set len (dupes dropped):", set.Len(), "has rust:", set.Has("rust"))

	fmt.Println("\n--- Generic alias ---")
	ps := StringPairs[int]{{"a", 1}, {"b", 2}}
	fmt.Printf("%+v\n", ps)

	// The stdlib is generic now: prefer these over hand-written helpers.
	words := []string{"banana", "apple", "cherry"}
	slices.Sort(words)
	fmt.Println("slices.Sort:", words)
	fmt.Println("cmp.Compare:", cmp.Compare(1, 2))
}
