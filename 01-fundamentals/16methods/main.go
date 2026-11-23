// Lesson 16: Methods
//
// A method is a function with a RECEIVER, declared on a type you own
// (defined in the same package):
//
//	func (r Rect) Area() float64   { ... }  // value receiver
//	func (c *Counter) Inc()        { ... }  // pointer receiver
//
// Value receiver vs pointer receiver:
//   - Value receiver: the method gets a COPY. It cannot change the caller's value.
//   - Pointer receiver: the method gets the address. It CAN mutate, and it does
//     not copy a large struct on each call.
//
// Method sets (what the spec says is "in" a type, which matters for interfaces,
// see lesson 17):
//   - The method set of T contains methods with a value receiver.
//   - The method set of *T contains methods with value AND pointer receivers.
//   - So a T value does NOT satisfy an interface that needs a pointer-receiver method.
//   - Calling a pointer method on an addressable variable is allowed shortcut
//     syntax: c.Inc() means (&c).Inc(). It does not work on map elements or
//     return values (they are not addressable).
//
// Which receiver to use (rules of thumb from the Go Code Review Comments):
//  1. If the method must mutate the receiver: pointer.
//  2. If the struct is large, or contains a sync.Mutex (must not be copied): pointer.
//  3. If any method of the type uses a pointer receiver, make ALL of them pointer
//     receivers, for consistency.
//  4. Small immutable values (time.Time, a Point, a Money struct), maps, funcs and
//     basic types: value receiver.
//
// You can define methods on any named type you own, not only structs:
//
//	type Celsius float64
//	func (c Celsius) Fahrenheit() float64 { ... }
//
// Gotcha: a method with a value receiver that "mutates" does nothing visible to
// the caller; the compiler will not warn you. Another trap is mixing receiver
// kinds on one type, which confuses method sets.
//
// In industry: services, repositories and clients are pointer-receiver types
// (NewServer returns *Server). Small value types (Money, Email, ID) use value
// receivers. Methods are mostly how you attach behaviour to data and make a
// type satisfy an interface.
package main

import (
	"fmt"
	"math"
)

// Rect is small and immutable-ish, so value receivers fit.
type Rect struct {
	W, H float64
}

// Area has a value receiver: it only reads.
func (r Rect) Area() float64 { return r.W * r.H }

// Scale with a value receiver cannot affect the caller. Shown as a gotcha.
func (r Rect) ScaleBroken(f float64) {
	r.W *= f
	r.H *= f
}

// Scale has a pointer receiver, so it can modify the original.
func (r *Rect) Scale(f float64) {
	r.W *= f
	r.H *= f
}

// Counter has state that changes, so every method uses a pointer receiver.
type Counter struct {
	n int
}

func (c *Counter) Inc()       { c.n++ }
func (c *Counter) Value() int { return c.n } // pointer receiver too: consistency

// Celsius shows a method on a non-struct named type.
type Celsius float64

func (c Celsius) Fahrenheit() float64 { return float64(c)*9/5 + 32 }

// Circle and the Shape interface show method sets in action.
type Circle struct{ R float64 }

func (c *Circle) Area() float64 { return math.Pi * c.R * c.R }

type Shape interface {
	Area() float64
}

func main() {
	fmt.Println("Methods in Go")

	r := Rect{W: 2, H: 3}
	fmt.Println("area:", r.Area())

	r.ScaleBroken(10)
	fmt.Println("after ScaleBroken (value receiver):", r)

	r.Scale(10) // r is addressable, so Go uses (&r).Scale(10)
	fmt.Println("after Scale (pointer receiver):", r)

	var c Counter
	for range 3 {
		c.Inc()
	}
	fmt.Println("counter:", c.Value())

	// A method value binds the receiver now.
	inc := c.Inc
	inc()
	inc()
	fmt.Println("counter after method value calls:", c.Value())

	// A method expression turns the method into a plain function.
	area := Rect.Area
	fmt.Println("method expression:", area(Rect{W: 4, H: 5}))

	fmt.Printf("%.1f°C = %.1f°F\n", 100.0, Celsius(100).Fahrenheit())

	// Method sets: *Circle has Area, Circle (the value) does not.
	var s Shape = &Circle{R: 1} // OK
	fmt.Printf("Shape area: %.2f\n", s.Area())
	// var bad Shape = Circle{R: 1} // compile error: Area has pointer receiver

	// Pointer methods cannot be called on a map element (not addressable).
	counters := map[string]*Counter{"a": {}} // store pointers to make this work
	counters["a"].Inc()
	fmt.Println("map of pointers:", counters["a"].Value())
}
