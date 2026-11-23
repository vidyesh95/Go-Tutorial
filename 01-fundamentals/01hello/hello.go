// Lesson 01: Hello, World
//
// Every Go program starts in package main, at the function main.
// The `go run` command compiles the package into a temporary binary and runs it:
//
//	go run ./01-fundamentals/01hello
//
// Key rules:
//   - A directory is one package. All .go files in it start with the same `package` line.
//   - `package main` + `func main()` makes an executable. Other package names make libraries.
//   - Imports are explicit. An unused import is a compile error (Go keeps code tidy on purpose).
//   - `fmt` is the standard-library package for formatted I/O.
//
// Gotcha: `go run main.go` only compiles that one file. Prefer `go run ./folder`
// so every file in the package is included.
//
// In industry: a real service's main() is tiny. It reads config, wires
// dependencies, starts the server and handles shutdown. The logic lives in
// other packages so it can be tested. Teams also use `go build` to produce a
// single static binary that is copied into a small container image.
package main

import "fmt"

func main() {
	fmt.Println("Hello, World!")
}
