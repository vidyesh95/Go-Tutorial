// Lesson 04: Variables and constants
//
// Ways to declare a variable:
//
//	var name string = "Ada" // full form
//	var name = "Ada"        // type inferred from the value
//	var count int           // no value: gets the ZERO VALUE (0)
//	name := "Ada"           // short declaration, only inside functions
//
// Key rules:
//   - Every variable has a zero value: 0, false, "", nil. There is no
//     "uninitialised" memory in Go.
//   - `:=` declares and assigns; `=` only assigns to something already declared.
//   - An unused local variable is a compile error. Use `_` to discard a value.
//   - A `const` is fixed at compile time. Untyped constants (such as `10`) adapt
//     to whatever type they are used with: `const n = 10` can be used as an int,
//     a float64 or a time.Duration without conversion.
//   - Scope: a name declared inside a block ({ }) is visible only in that block.
//     Shadowing (re-declaring with := in an inner block) is legal but a classic
//     source of bugs.
//
// Gotcha: do NOT keep secrets such as tokens or passwords in source code.
// The old version of this lesson did that purely as a demo.
//
// In industry: configuration (API keys, URLs) comes from environment variables
// or a secret manager, read once at start-up. Package-level `var` is used
// sparingly, because global mutable state makes tests and concurrency hard.
// Package-level `const` is common for fixed values such as header names.
package main

import (
	"fmt"
	"os"
)

// AppName is an exported, package-level constant. Capital letter = exported.
const AppName string = "go-tutorial"

// maxRetries is an unexported constant (lower-case) and its type is inferred.
const maxRetries = 3

func main() {
	fmt.Println("Variables in Go")
	fmt.Println("constant:", AppName, "| type:", fmt.Sprintf("%T", AppName), "| len:", len(AppName))
	fmt.Println("maxRetries:", maxRetries)

	// Full form.
	var username string = "ada"
	fmt.Println("username:", username)

	// Short declaration. The compiler infers int.
	k := 10
	fmt.Printf("k: type=%T value=%v\n", k, k)

	// Zero values: declared but not assigned.
	var count int
	var ready bool
	var label string
	fmt.Printf("zero values: %d %t %q\n", count, ready, label)

	// A grouped declaration block.
	var (
		a = 1
		b = 2
		c = 3
	)
	fmt.Println("a, b, c:", a, b, c)

	// Multiple assignment, including swapping without a temp variable.
	a, b = b, a
	fmt.Println("after swap:", a, b)

	// In industry, config comes from the environment, with a default.
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	fmt.Println("port:", port)

	// Shadowing: the inner x is a NEW variable that hides the outer one.
	x := "outer"
	if true {
		x := "inner"
		fmt.Println("inside block:", x)
	}
	fmt.Println("outside block:", x)
}
