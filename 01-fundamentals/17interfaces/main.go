// Lesson 17: Interfaces
//
// An interface is a set of method signatures. ANY type that has those methods
// satisfies the interface automatically. There is no `implements` keyword.
// This is called implicit (structural) satisfaction.
//
//	type Reader interface { Read(p []byte) (n int, err error) }
//
// Key rules:
//   - Keep interfaces SMALL: one to three methods. io.Reader, io.Writer,
//     fmt.Stringer and error each have a single method and are hugely reusable.
//     "The bigger the interface, the weaker the abstraction." (Go Proverbs)
//   - Define interfaces where they are USED (the consumer), not where the
//     concrete type lives. The producer just returns a concrete type.
//   - "Accept interfaces, return structs": parameters should be as general as
//     possible; return values should be concrete so callers get every method
//     and you can add methods without breaking anyone.
//   - `any` is an alias for `interface{}`: an interface with no methods, so
//     every type satisfies it. It gives up compile-time type safety, so use it
//     only when the type is truly unknown (e.g. decoding JSON of unknown shape).
//   - Type assertion: `v, ok := x.(T)`. The single-value form `x.(T)` PANICS if
//     x does not hold a T. Use the comma-ok form for anything you do not control.
//   - Type switch: `switch v := x.(type) { case int: ... }` branches on the
//     dynamic type.
//   - An interface value is a pair: (dynamic type, dynamic value).
//
// The nil interface gotcha:
//
//	An interface is nil ONLY if both the type and the value are nil. If you
//	store a nil *MyErr pointer into an `error` variable, the interface holds
//	(type=*MyErr, value=nil), which is NOT == nil. Returning a typed nil
//	pointer as an error is a classic bug: the caller sees err != nil.
//	Fix: return a literal `nil` when there is no error.
//
// In industry: interfaces are the seams for testing. A handler takes a small
// `UserStore` interface, production passes a Postgres-backed struct, and tests
// pass a fake. The standard library's io.Reader and io.Writer make files, HTTP
// bodies, buffers and gzip streams interchangeable.
package main

import (
	"fmt"
	"io"
	"math"
	"strings"
)

// Shape is small and defined by the consumer of shapes.
type Shape interface {
	Area() float64
}

// Rect and Circle never mention Shape; they satisfy it by having Area().
type Rect struct{ W, H float64 }
type Circle struct{ R float64 }

func (r Rect) Area() float64   { return r.W * r.H }
func (c Circle) Area() float64 { return math.Pi * c.R * c.R }

// String makes Rect satisfy fmt.Stringer: fmt uses it automatically.
func (r Rect) String() string { return fmt.Sprintf("Rect(%gx%g)", r.W, r.H) }

// totalArea ACCEPTS an interface: works with any Shape, now and in the future.
func totalArea(shapes ...Shape) float64 {
	total := 0.0
	for _, s := range shapes {
		total += s.Area()
	}
	return total
}

// countBytes accepts io.Reader, so it works with strings, files, HTTP bodies...
func countBytes(r io.Reader) (int, error) {
	data, err := io.ReadAll(r)
	return len(data), err
}

// NewRect RETURNS a concrete type, not an interface.
func NewRect(w, h float64) Rect { return Rect{W: w, H: h} }

// describe uses a type switch on `any`.
func describe(x any) string {
	switch v := x.(type) {
	case nil:
		return "nil"
	case int:
		return fmt.Sprintf("int %d", v)
	case string:
		return fmt.Sprintf("string %q (len %d)", v, len(v))
	case []int:
		return fmt.Sprintf("slice of %d ints", len(v))
	case fmt.Stringer:
		return "Stringer " + v.String()
	case error:
		return "error " + v.Error()
	default:
		return fmt.Sprintf("other %T", v)
	}
}

// MyErr and the two functions below demonstrate the nil interface gotcha.
type MyErr struct{}

func (*MyErr) Error() string { return "my error" }

func brokenCheck() error {
	var e *MyErr // nil pointer
	return e     // BUG: returns a NON-nil error (type *MyErr, value nil)
}

func correctCheck() error {
	return nil // a literal nil: the interface itself is nil
}

func main() {
	fmt.Println("Interfaces in Go")

	// Implicit satisfaction.
	var s Shape = NewRect(2, 3)
	fmt.Println("Rect area:", s.Area())
	fmt.Printf("total area: %.2f\n", totalArea(Rect{1, 1}, Circle{1}))

	// fmt.Stringer is used by Println.
	fmt.Println("Stringer in action:", NewRect(4, 5))

	// io.Reader: one tiny interface, many sources.
	n, _ := countBytes(strings.NewReader("hello reader"))
	fmt.Println("bytes read:", n)

	// Type assertion, comma-ok form.
	if r, ok := s.(Rect); ok {
		fmt.Println("it is a Rect with width", r.W)
	}
	if _, ok := s.(Circle); !ok {
		fmt.Println("it is not a Circle")
	}

	// Interface-to-interface assertion: "does this value ALSO have String()?"
	if st, ok := s.(fmt.Stringer); ok {
		fmt.Println("s is also a Stringer:", st)
	}

	// Type switch over any.
	for _, v := range []any{42, "hi", []int{1, 2}, NewRect(1, 2), io.EOF, 3.5, nil} {
		fmt.Println("describe:", describe(v))
	}

	// The nil interface gotcha.
	fmt.Println("brokenCheck() == nil:", brokenCheck() == nil, "<- surprising")
	fmt.Println("correctCheck() == nil:", correctCheck() == nil)

	// A single-value assertion that fails panics. We recover only to show it.
	func() {
		defer func() { fmt.Println("recovered:", recover()) }()
		var x any = "text"
		_ = x.(int) // panics: interface conversion
	}()
}
