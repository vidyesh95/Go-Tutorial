// Lesson 07: Pointers
//
// A pointer holds the memory address of a value.
//
//	&x   address-of: gives a pointer to x
//	*p   dereference: reads or writes the value p points to
//	*T   the type "pointer to T"
//
// Key rules:
//   - The zero value of a pointer is nil. Dereferencing nil panics at run time.
//   - Go has NO pointer arithmetic. Pointers are for sharing and mutating,
//     not for walking memory.
//   - Everything in Go is passed by value. Passing a pointer copies the ADDRESS,
//     so the callee can modify the original value.
//   - It is safe to return a pointer to a local variable. The compiler moves
//     it to the heap automatically (escape analysis). There are no dangling pointers.
//   - Pointers to structs auto-dereference: p.Name works instead of (*p).Name.
//   - Go 1.26 lets new() take a VALUE: new(42) returns *int pointing at 42.
//     Before that you needed a temp variable or a helper like ptr(v).
//
// Gotcha: slices, maps and channels already contain a reference internally,
// so you almost never need a pointer to them.
//
// In industry: pointers are used for (1) methods that must mutate their
// receiver (lesson 16), (2) large structs you do not want to copy, and
// (3) optional values, where nil means "absent". JSON APIs often use
// *string, *int or *time.Time to tell "missing" apart from "zero".
// new(expr) makes building such optional fields a one-liner.
package main

import "fmt"

// incrementByValue gets a COPY of n, so the caller's value is unchanged.
func incrementByValue(n int) {
	n++
}

// incrementByPointer gets the address, so it changes the caller's value.
func incrementByPointer(n *int) {
	*n++
}

// newCounter returns a pointer to a local variable. This is safe in Go.
func newCounter() *int {
	c := 100
	return &c
}

func main() {
	fmt.Println("Pointers in Go")

	username := "Ada"
	p := &username // p has type *string
	fmt.Println("username:", username)
	fmt.Println("*p:", *p)
	fmt.Println("p == &username:", p == &username)

	// Writing through the pointer changes the original variable.
	*p = "Ada Lovelace"
	fmt.Println("after *p = ...:", username)

	// Pass by value vs pass by pointer.
	n := 1
	incrementByValue(n)
	fmt.Println("after incrementByValue:", n)
	incrementByPointer(&n)
	fmt.Println("after incrementByPointer:", n)

	// Returning a pointer to a local is fine.
	c := newCounter()
	*c++
	fmt.Println("counter:", *c)

	// The zero value is nil. Always check before dereferencing if it may be nil.
	var missing *int
	fmt.Println("missing == nil:", missing == nil)
	if missing != nil {
		fmt.Println(*missing)
	}

	// Go 1.26: new(expr) allocates a variable initialised to the value.
	age := new(30)
	title := new("engineer")
	fmt.Println("new(30):", *age, "| new(\"engineer\"):", *title)

	// Typical use: an optional field where nil means "not provided".
	type Update struct {
		Name *string // nil = leave unchanged
		Age  *int
	}
	u := Update{Name: new("Grace")} // Age stays nil
	fmt.Println("update sets name:", u.Name != nil, "| sets age:", u.Age != nil)
}
