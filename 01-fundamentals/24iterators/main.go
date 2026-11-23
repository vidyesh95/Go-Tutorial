// Lesson 24: Iterators and range-over-func (Go 1.23+)
//
// An ITERATOR is just a function that hands values to a callback named `yield`:
//
//	iter.Seq[V]     = func(yield func(V) bool)
//	iter.Seq2[K, V] = func(yield func(K, V) bool)
//
// You consume one with a plain `for ... range`:
//
//	for v := range seq { ... }
//
// Key rules:
//   - yield returns false when the loop body did `break` / `return`. Your iterator
//     MUST stop calling yield after that (just `return`), or the runtime panics.
//   - Iterators are lazy: nothing runs until somebody ranges over them. That makes
//     them great for large or infinite sequences and for paginated data.
//   - Standard library helpers return iterators instead of slices:
//     slices.All / slices.Values / slices.Collect / slices.Sorted,
//     maps.Keys / maps.Values / maps.All,
//     strings.SplitSeq / strings.Lines / strings.FieldsSeq (Go 1.24).
//   - iter.Pull converts a "push" iterator into a "pull" one (next/stop) for the
//     rare cases where you must advance two sequences in lockstep. It uses
//     coroutines under the hood, so prefer plain range whenever possible.
//   - Name constructors after what they produce and return iter.Seq, so callers
//     read naturally: `for n := range Fibonacci()`.
//
// In industry: iterators appear wherever data arrives in chunks: walking database
// rows, paging through a REST API (cursor/next-page tokens), streaming lines
// from a log file, or walking a tree. The caller gets a simple `for` loop and the
// producer controls fetching, cleanup (defer inside the iterator) and early exit.
package main

import (
	"fmt"
	"iter"
	"maps"
	"slices"
	"strings"
)

// Fibonacci is an INFINITE iterator. Safe because it is lazy and stops on break.
func Fibonacci() iter.Seq[int] {
	return func(yield func(int) bool) {
		a, b := 0, 1
		for {
			if !yield(a) {
				return // consumer broke out of the loop: stop immediately
			}
			a, b = b, a+b
		}
	}
}

// Take limits any iterator to the first n values (an iterator adapter).
func Take[V any](seq iter.Seq[V], n int) iter.Seq[V] {
	return func(yield func(V) bool) {
		if n <= 0 {
			return
		}
		count := 0
		for v := range seq {
			if !yield(v) {
				return
			}
			count++
			if count >= n {
				return
			}
		}
	}
}

// ---- Simulated paginated API ----------------------------------------------

// fetchPage pretends to call GET /items?page=N. Each page holds up to 3 items.
func fetchPage(page int) (items []string, hasMore bool) {
	all := []string{"a", "b", "c", "d", "e", "f", "g"}
	start := page * 3
	if start >= len(all) {
		return nil, false
	}
	end := min(start+3, len(all))
	fmt.Printf("    (fetching page %d)\n", page)
	return all[start:end], end < len(all)
}

// AllItems hides pagination: callers just range over items.
// Pages are only fetched as needed, so an early break saves network calls.
func AllItems() iter.Seq[string] {
	return func(yield func(string) bool) {
		for page := 0; ; page++ {
			items, hasMore := fetchPage(page)
			for _, it := range items {
				if !yield(it) {
					return
				}
			}
			if !hasMore {
				return
			}
		}
	}
}

// Enumerate yields index/value pairs: an iter.Seq2 example.
func Enumerate[V any](seq iter.Seq[V]) iter.Seq2[int, V] {
	return func(yield func(int, V) bool) {
		i := 0
		for v := range seq {
			if !yield(i, v) {
				return
			}
			i++
		}
	}
}

func main() {
	fmt.Println("--- Custom iterator: Fibonacci (infinite, early break) ---")
	for n := range Fibonacci() {
		if n > 50 {
			break // yield returns false inside Fibonacci; it returns cleanly
		}
		fmt.Print(n, " ")
	}
	fmt.Println()
	fmt.Println("Take(Fibonacci, 8):", slices.Collect(Take(Fibonacci(), 8)))

	fmt.Println("\n--- Paginated fetch, stop early ---")
	for item := range AllItems() {
		fmt.Println("  got", item)
		if item == "d" {
			break // pages 2 and 3 are never fetched
		}
	}

	fmt.Println("\n--- Seq2 ---")
	for i, v := range Enumerate(Take(AllItems(), 2)) {
		fmt.Println("  ", i, v)
	}

	fmt.Println("\n--- slices package ---")
	fruits := []string{"pear", "apple", "fig"}
	for i, f := range slices.All(fruits) { // index, value
		fmt.Print(i, "=", f, " ")
	}
	fmt.Println()
	for v := range slices.Values(fruits) { // values only
		fmt.Print(v, " ")
	}
	fmt.Println()
	fmt.Println("Sorted copy:", slices.Sorted(slices.Values(fruits)))
	fmt.Println("Original   :", fruits)

	fmt.Println("\n--- maps package ---")
	ages := map[string]int{"bob": 30, "alice": 25, "carol": 41}
	// Map iteration order is random; Sorted(Keys) gives deterministic output.
	keys := slices.Sorted(maps.Keys(ages))
	fmt.Println("Sorted keys:", keys)
	total := 0
	for _, age := range maps.All(ages) {
		total += age
	}
	fmt.Println("Sum of ages:", total)
	fmt.Println("Collect map copy:", maps.Collect(maps.All(ages)))

	fmt.Println("\n--- strings iterators (no intermediate slice) ---")
	for part := range strings.SplitSeq("a,b,c", ",") {
		fmt.Print("[", part, "]")
	}
	fmt.Println()
	text := "line one\nline two\nline three\n"
	for line := range strings.Lines(text) { // keeps the trailing \n
		fmt.Printf("%q ", line)
	}
	fmt.Println()

	fmt.Println("\n--- iter.Pull: pull values manually ---")
	next, stop := iter.Pull(Take(Fibonacci(), 5))
	defer stop() // always call stop to release the coroutine
	for {
		v, ok := next()
		if !ok {
			break
		}
		fmt.Print(v, " ")
	}
	fmt.Println()
}
