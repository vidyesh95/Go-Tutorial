// Lesson 12: Maps
//
// A map is an unordered collection of key/value pairs (a hash table).
//
//	m := make(map[string]int)               // empty, ready to use
//	m := map[string]int{"a": 1, "b": 2}     // literal
//	v := m["a"]                             // missing key gives the ZERO VALUE
//	v, ok := m["a"]                         // "comma ok": ok is false if missing
//	m["c"] = 3                              // insert or update
//	delete(m, "a")                          // remove (no-op if absent)
//	clear(m)                                // remove everything
//
// Key rules:
//   - Keys must be comparable (==): strings, numbers, bools, arrays, structs of
//     those. Slices, maps and functions cannot be keys.
//   - The zero value of a map is nil. Reading from a nil map is fine, but
//     WRITING to a nil map panics. Always create it with make or a literal.
//   - Iteration order is deliberately RANDOM. Never rely on it. To print or
//     process in a stable order, collect the keys and sort them.
//   - Maps are NOT safe for concurrent use. Guard them with a sync.Mutex or use
//     sync.Map for special cases. The runtime may crash on concurrent write.
//   - Maps are reference-like: passing a map to a function shares the same data.
//   - A struct with a map field is not comparable with ==. Use maps.Equal.
//
// Gotcha: `m[k].field = x` does not compile when the value is a struct. Read,
// modify the copy, then store it back (or store pointers).
//
// In industry: maps are used for lookups, caches, counting, and sets
// (map[T]struct{} or map[T]bool). Deterministic output (logs, JSON, tests) is
// achieved with slices.Sorted(maps.Keys(m)). A map that many goroutines touch
// needs a mutex.
package main

import (
	"fmt"
	"maps"
	"slices"
)

func main() {
	fmt.Println("Maps in Go")

	ages := make(map[string]int)
	ages["alice"] = 30
	ages["bob"] = 25
	// fmt prints maps sorted by key, which is why printing a map looks stable.
	fmt.Println("ages:", ages, "| len:", len(ages))

	// A missing key returns the zero value, which can be confusing.
	fmt.Println("carol (missing):", ages["carol"])

	// Comma-ok tells "present with zero value" apart from "absent".
	if age, ok := ages["carol"]; !ok {
		fmt.Println("carol is not in the map, age:", age)
	}
	if age, ok := ages["alice"]; ok {
		fmt.Println("alice is", age)
	}

	delete(ages, "bob")
	fmt.Println("after delete:", ages)

	// Writing to a nil map panics. Reading is fine.
	var nilMap map[string]int
	fmt.Println("read from nil map:", nilMap["x"], "| len:", len(nilMap))

	// Literal.
	stock := map[string]int{"apple": 5, "pear": 0, "plum": 12}

	// Deterministic iteration: sort the keys first.
	for _, k := range slices.Sorted(maps.Keys(stock)) {
		fmt.Println("  ", k, "=>", stock[k])
	}

	// maps.Keys and maps.Values return iterators (Go 1.23+).
	fmt.Println("values sorted:", slices.Sorted(maps.Values(stock)))

	// maps helpers.
	copyOfStock := maps.Clone(stock)
	fmt.Println("Equal after clone:", maps.Equal(stock, copyOfStock))
	maps.DeleteFunc(copyOfStock, func(_ string, qty int) bool { return qty == 0 })
	fmt.Println("DeleteFunc removed out-of-stock:", copyOfStock)

	// Counting words: the zero value makes this elegant.
	counts := make(map[rune]int)
	for _, r := range "hello" {
		counts[r]++ // missing key starts at 0
	}
	for _, r := range slices.Sorted(maps.Keys(counts)) {
		fmt.Printf("  %c=%d", r, counts[r])
	}
	fmt.Println()

	// A set is a map with empty-struct values (struct{} uses zero memory).
	seen := map[string]struct{}{}
	for _, w := range []string{"go", "rust", "go", "zig"} {
		seen[w] = struct{}{}
	}
	fmt.Println("unique words:", slices.Sorted(maps.Keys(seen)))

	// Maps of slices: append to a missing key works because nil slices are valid.
	groups := map[int][]string{}
	for _, w := range []string{"go", "rust", "c", "zig", "java"} {
		groups[len(w)] = append(groups[len(w)], w)
	}
	fmt.Println("grouped by length:", groups)

	// clear empties the map but keeps it usable.
	clear(stock)
	fmt.Println("after clear:", stock, "| len:", len(stock))
}
