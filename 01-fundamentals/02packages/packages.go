// Lesson 02: Packages and imports
//
// A package is a directory of .go files that are compiled together.
// Every Go program is built from packages; execution starts in package main.
//
// Key rules:
//   - The package name is, by convention, the last element of the import path.
//     "math/rand/v2" is the exception: its package name is rand (the /v2 is a
//     major-version suffix, not a name).
//   - A name is exported (visible to other packages) if it starts with a capital
//     letter: math.Pi is exported, math.pi would not be accessible.
//   - There are no public/private keywords. Capitalisation is the whole mechanism.
//   - You may alias an import: `import r "math/rand/v2"`. Use it only to avoid clashes.
//
// Gotcha: math/rand (v1) is the old API. New code should use math/rand/v2,
// which has nicer function names (rand.IntN) and better generators. Neither
// is suitable for secrets or tokens. Use crypto/rand for those.
//
// In industry: package layout is an architectural decision. Teams keep
// packages small and named for what they provide ("billing", "auth"), not
// generic names such as "util" or "common". Exporting less is good: every
// capitalised name is an API you must keep stable.
package main

import (
	"fmt"
	"math"
	"math/rand/v2"
)

func main() {
	fmt.Println("Packages in Go")

	// rand.IntN(10) returns a number in [0, 10). Output differs on each run.
	fmt.Println("My favorite number is", rand.IntN(10))

	// math.Pi is an exported constant.
	fmt.Println("Pi:", math.Pi)

	// math.Sqrt is an exported function.
	fmt.Println("Sqrt(16):", math.Sqrt(16))
}
