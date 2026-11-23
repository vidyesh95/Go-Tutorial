// Lesson 29: Modules and packages
//
// VOCABULARY
//   - Package: a directory of .go files sharing one `package` clause. The unit of code
//     organisation and of visibility.
//   - Module: a tree of packages versioned together, defined by a go.mod file at its root.
//     This whole repo is ONE module (see ../../go.mod): github.com/vidyesh95/Go-Tutorial.
//   - Module path: the import-path prefix, usually the repository URL. A package's import
//     path = module path + its directory, e.g.
//     github.com/vidyesh95/Go-Tutorial/01-fundamentals/29modules/mathx
//
// FILES
//   - go.mod: module path, minimum Go version, dependencies (require), and optional
//     replace / exclude / tool directives.
//   - go.sum: checksums of every dependency version, so builds are reproducible and
//     tampering is detected. Commit it. Never edit it by hand.
//
// COMMANDS
//   - go mod init <path>   create go.mod
//   - go get pkg@version   add/upgrade a dependency (go get pkg@latest, go get pkg@v1.2.3)
//   - go mod tidy          add missing and remove unused requirements; run before committing
//   - go mod download      fetch dependencies into the module cache
//   - go list -m all       show the full dependency graph
//   - go tool <name>       run a tool pinned in go.mod via the `tool` directive (Go 1.24+):
//     `go get -tool github.com/sqlc-dev/sqlc/cmd/sqlc` then `go tool sqlc generate`.
//     This replaces the old "tools.go with blank imports" trick.
//
// VISIBILITY
//   - Identifiers starting with an Uppercase letter are EXPORTED (visible to other packages).
//     Lowercase are unexported (package-private). There is no private/public keyword.
//   - A directory named `internal` restricts importers to the code rooted at its parent.
//     The compiler enforces it. (See internal/greet.)
//
// INITIALISATION ORDER
//  1. Imported packages are initialised first (dependencies before dependents, each once).
//  2. Within a package: package-level variables (in dependency order), then init() functions.
//  3. Finally main.main runs.
//     Avoid init() for anything with side effects; explicit constructors are easier to test.
//
// VERSIONING
//   - Modules use semantic versioning: vMAJOR.MINOR.PATCH.
//   - Semantic import versioning: from v2 onward the major version is part of the module
//     path AND the import path (github.com/me/lib/v2). v0/v1 have no suffix. This lets one
//     program depend on lib v1 and lib v2 at the same time. Breaking changes require a
//     new major version, so Go libraries keep APIs very stable.
//   - Minimal Version Selection: Go picks the MINIMUM version satisfying all requirements,
//     not the newest, so builds are predictable.
//
// WORKSPACES (go.work)
//   - `go work init ./api ./shared` creates go.work so several local modules build together
//     without `replace` directives. Useful when editing a library and its consumer at the
//     same time. Generally do not commit go.work for libraries.
//
// In industry: a backend team usually has one module per service (or one monorepo module),
// pins dependencies through go.mod/go.sum, runs `go mod tidy` and checks for a clean git
// diff in CI, keeps business logic under internal/ so it cannot be imported accidentally,
// and uses `go tool` for code generators, linters and migration tools.
//
// Run: go run ./01-fundamentals/29modules
package main

import (
	"fmt"

	"github.com/vidyesh95/Go-Tutorial/01-fundamentals/29modules/internal/greet"
	"github.com/vidyesh95/Go-Tutorial/01-fundamentals/29modules/mathx"
)

func init() {
	// main's own init runs after the imported packages' inits, right before main().
	fmt.Println("init: package main")
}

func main() {
	fmt.Println("--- main starts ---")

	// Exported identifiers: accessible via the package name.
	fmt.Println(greet.Hello("Gopher"))
	fmt.Println("mathx.Pi     =", mathx.Pi)
	fmt.Println("mathx.Square =", mathx.Square(7))

	var c mathx.Counter
	c.Inc()
	fmt.Println("Counter.Inc  =", c.Inc())

	// Unexported identifiers: uncommenting any line below is a COMPILE error.
	// fmt.Println(mathx.secret)      // undefined: mathx.secret (not exported)
	// fmt.Println(mathx.mul(2, 3))   // undefined: mathx.mul (not exported)
	// fmt.Println(c.n)               // c.n undefined (cannot refer to unexported field)
	fmt.Println("mathx.Secret() =", mathx.Secret(), "(controlled access to an unexported var)")
}
