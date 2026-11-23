// Package greet lives under an internal/ directory.
//
// Go enforces that a package inside .../29modules/internal/... can only be imported by
// code rooted at .../29modules/ (the parent of "internal"). Code outside that tree
// (other lessons, other modules) gets a compile error. Use internal/ to keep helper
// code private so you can change it without breaking anyone.
package greet

import "fmt"

// init runs automatically when the package is first loaded, before main.
// A package can have several init functions; they run in the order the files are
// presented to the compiler (alphabetical by file name with the go tool).
func init() {
	fmt.Println("init: package greet")
}

// Hello is exported (capital H): importable by other packages.
func Hello(name string) string {
	return prefix() + ", " + name + "!"
}

// prefix is unexported (lowercase): only code inside package greet can call it.
func prefix() string { return "Hello" }
