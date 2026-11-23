# 01 - Go Fundamentals

Thirty short, runnable lessons that take you from "Hello, World" to the modern Go
you will meet in a backend job. Each lesson is one folder with a single `.go`
file. The top of the file is a doc comment that explains the concept, the rules,
the gotchas, and finishes with an "In industry:" note about how real teams use it.

## Running a lesson

From the repository root:

```sh
go run ./01-fundamentals/01hello
go run ./01-fundamentals/17interfaces
```

Every lesson is a `package main` that terminates quickly. Open the file, read
the comment at the top, then run it and compare the output with the code.

Useful commands while studying:

```sh
gofmt -l ./01-fundamentals     # lists badly formatted files (prints nothing when clean)
go vet ./01-fundamentals/...   # static checks
go doc strings.Cut             # read standard-library docs offline
```

## Lessons

| #  | Folder          | What you learn |
|----|-----------------|----------------|
| 01 | `01hello`       | A first program: `package main`, `func main`, `go run`. |
| 02 | `02packages`    | Packages, imports, exported names (capital letters), `math/rand/v2`. |
| 03 | `03functions`   | Parameters, multiple and named results, the `(value, error)` pattern, `min`/`max`. |
| 04 | `04variables`   | `var`, `:=`, zero values, constants, scope and shadowing. |
| 05 | `05datatypes`   | Integers, floats, strings, `byte`/`rune`, conversions, overflow, why not floats for money. |
| 06 | `06operators`   | Arithmetic, comparison, logical and bitwise operators, short-circuiting. |
| 07 | `07pointers`    | `&` and `*`, pass-by-value vs pointer, nil, and `new(expr)` for optional values. |
| 08 | `08loops`       | The single `for` keyword, `range` over int, labels, per-iteration loop variables. |
| 09 | `09conditions`  | `if` with init statements, `switch` (no fallthrough, tagless), early returns. |
| 10 | `10arrays`      | Fixed-size arrays are values; slicing an array; arrays as map keys. |
| 11 | `11slices`      | Length vs capacity, `append`, shared memory, aliasing, the `slices` package. |
| 12 | `12maps`        | Maps, comma-ok, nil maps, sets, deterministic output with `maps` and `slices.Sorted`. |
| 13 | `13range`       | Ranging over slices, maps, strings (runes), channels, ints and iterators. |
| 14 | `14strings`     | Bytes vs runes, `strings.Builder`, `Cut`/`CutLast`, `Fields`, `SplitSeq`, `Lines`, `utf8`, `strconv`. |
| 15 | `15structs`     | Struct definitions, literals, zero values, anonymous structs, comparison, tags. |
| 16 | `16methods`     | Value vs pointer receivers, method sets, when to use pointer receivers. |
| 17 | `17interfaces`  | Implicit satisfaction, small interfaces, `any`, assertions, type switches, the nil-interface trap. |
| 18 | `18enums`       | `iota`, typed constants, `String()`, validating enum values, bit flags. |
| 19 | `19embedding`   | Struct and interface embedding, promoted methods, composition over inheritance. |
| 20 | `20errors`      | `errors.New`, `%w` wrapping, sentinels, custom types, `Is`/`As`/`AsType`, `Join`. |
| 21 | `21defer`       | `defer` order and gotchas, `panic`, `recover`, when panic is acceptable. |
| 22 | `22closures`    | Function values, closures, variadic functions, higher-order functions, functional options. |
| 23 | `23generics`    | Type parameters, constraints, generic functions and types, when not to use generics. |
| 24 | `24iterators`   | `iter.Seq`/`iter.Seq2`, range-over-func, building and composing iterators. |
| 25 | `25time`        | `time.Time`, `Duration`, formatting and parsing, time zones, timers. |
| 26 | `26files`       | Reading and writing files, `os`, `io`, `bufio`, paths, `os.Root`, `embed`. |
| 27 | `27json`        | `encoding/json`: struct tags, marshalling, decoding, `omitzero`, unknown fields, `encoding/json/v2`. |
| 28 | `28testing`     | `go test`, table-driven tests, subtests, benchmarks (`b.Loop`), fuzzing, examples, coverage. |
| 29 | `29modules`     | `go.mod`, dependencies, versions, `internal/` packages, `init()`, workspaces, the `tool` directive. |
| 30 | `30modern-go`   | A tour of recent Go features: `uuid`, `new(expr)`, `errors.AsType`, `CutLast`, and more. |

Suggested order: follow the numbers. Lessons 01-13 are the core syntax. Lessons
14-22 are the ideas that separate "I wrote some Go" from "I can work on a Go
codebase": strings, structs, methods, interfaces, errors and defer. Lessons 23-30
add the tooling and standard-library skills expected in day-to-day backend work.

## Interview questions

1. **What is the zero value, and why does it matter?**
   Every variable is initialised: `0`, `false`, `""`, and `nil` for pointers,
   slices, maps, channels, functions and interfaces. Good Go types are designed
   so the zero value is immediately usable (for example `sync.Mutex`, `bytes.Buffer`,
   a nil slice you can `append` to).

2. **What is the difference between an array and a slice?**
   An array has a fixed length that is part of its type and is copied on
   assignment. A slice is a small header (pointer, length, capacity) that views
   an underlying array. Passing a slice copies the header, not the data, so
   functions can change elements but cannot grow the caller's slice.

3. **Why can appending to a slice surprise you?**
   `append` reuses the backing array while capacity allows, so two slices
   derived from the same base may overwrite each other's elements. When capacity
   is exceeded, `append` allocates a new array and the slices diverge. Always assign
   the result (`s = append(s, x)`) and use `slices.Clone` or a three-index slice
   when you need independence.

4. **What happens when you write to a nil map? Read from one?**
   Writing panics. Reading returns the zero value, and `len` is 0. Create maps with
   `make` or a literal. Map iteration order is random, so sort the keys
   (`slices.Sorted(maps.Keys(m))`) for deterministic output. Maps are not safe for
   concurrent writes.

5. **Value receiver or pointer receiver?**
   Use a pointer receiver when the method mutates the receiver, when the struct is
   large, or when it contains a mutex. Keep all methods of one type consistent. The
   method set of `T` has only value-receiver methods; the method set of `*T` has both,
   which decides whether `T` or `*T` satisfies an interface.

6. **How do interfaces work in Go, and what does "accept interfaces, return structs" mean?**
   A type satisfies an interface implicitly by having its methods. Define small
   interfaces on the consumer side and take them as parameters (flexible, easy to
   fake in tests), but return concrete types so callers keep full access and you can
   evolve the API without breaking them.

7. **Why can an interface holding a nil pointer be `!= nil`?**
   An interface value is a (type, value) pair, and it is nil only when both are nil.
   Returning a nil `*MyErr` as an `error` yields (type `*MyErr`, value nil), which
   is not nil. Return a literal `nil` for "no error".

8. **How should errors be handled in Go?**
   Return them as the last value and check them. Add context at each layer with
   `fmt.Errorf("doing x: %w", err)`, inspect with `errors.Is` (sentinel values) and
   `errors.As`/`errors.AsType` (typed errors), handle each error once (return or log,
   not both), and reserve `panic` for programmer bugs or impossible states.

9. **How does `defer` behave, and what is a common pitfall?**
   Deferred calls run when the function returns, in last-in-first-out order, and their
   arguments are evaluated at the `defer` statement. A defer inside a loop runs only
   when the whole function returns, so many resource handles can pile up; move the
   loop body into its own function. `recover` works only inside a deferred function.

10. **What is a closure, and what changed about loop variables in Go 1.22?**
    A closure is a function value that captures variables from its enclosing scope
    and keeps them alive. Before Go 1.22 all iterations shared one loop variable, so
    closures or goroutines created in a loop all saw the last value. Since Go 1.22
    each iteration has its own copy, so the `i := i` workaround is unnecessary.
