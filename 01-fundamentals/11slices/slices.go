// Lesson 11: Slices
//
// A slice is a flexible, dynamically sized view onto an underlying array.
// It is by far the most used collection in Go. Internally a slice is a small
// struct: (pointer to array, length, capacity).
//
//	var s []int            // nil slice: len 0, cap 0
//	s = []int{1, 2, 3}     // literal
//	s = make([]int, 3)     // len 3, cap 3, zero-filled
//	s = make([]int, 0, 10) // len 0, cap 10: room to append without reallocating
//	s = append(s, 4, 5)    // may return a NEW slice, always assign the result
//
// Key rules:
//   - len is how many elements you can see; cap is how many fit before append
//     must allocate a bigger array.
//   - A nil slice is usable: len(nil) == 0, append and range work on it.
//     Prefer `var s []T` over `[]T{}` unless you need non-nil (e.g. JSON "[]").
//   - Slices are not comparable with == (except to nil). Use slices.Equal.
//   - Sub-slicing (s[a:b]) shares memory with the original. Writes through one
//     are visible through the other. Use slices.Clone or copy for independence.
//   - Passing a slice to a function copies the header, not the elements: the
//     callee can change elements but cannot grow the caller's slice.
//
// Gotcha: append may or may not reuse the same backing array. If you append to a
// sub-slice that still has spare capacity, you can overwrite the original's data.
// When in doubt, clone, or use the full slice expression s[a:b:b] to cap the capacity.
//
// In industry: preallocate with make([]T, 0, n) when you know the size, and
// use the `slices` package (Contains, Index, Sort, SortFunc, BinarySearch,
// Reverse, Max, Min, Delete, Insert, Clone, Equal) instead of hand-written
// loops. Returning nil for "empty" is idiomatic.
package main

import (
	"fmt"
	"slices"
	"strings"
)

func main() {
	fmt.Println("Slices in Go")

	// nil slice.
	var nums []int
	fmt.Println("nil slice:", nums, "| is nil:", nums == nil, "| len:", len(nums), "| cap:", cap(nums))

	// make: length 3, so three zero values.
	letters := make([]string, 3)
	fmt.Printf("make: %q len=%d cap=%d\n", letters, len(letters), cap(letters))
	letters[0], letters[1], letters[2] = "a", "b", "c"

	// append returns the updated slice: always assign it back.
	letters = append(letters, "d")
	letters = append(letters, "e", "f")
	fmt.Println("after append:", letters, "len:", len(letters))

	// Watch capacity grow as we append one at a time.
	var grow []int
	lastCap := -1
	for i := range 20 {
		grow = append(grow, i)
		if cap(grow) != lastCap {
			lastCap = cap(grow)
			fmt.Printf("  len=%2d cap=%2d\n", len(grow), cap(grow))
		}
	}

	// Slicing shares memory.
	view := letters[1:4]
	fmt.Println("letters[1:4]:", view)
	view[0] = "B"
	fmt.Println("shared: letters now", letters)

	// Independent copies.
	dup := slices.Clone(letters)
	dup[0] = "Z"
	fmt.Println("clone changed:", dup, "| original:", letters)

	// copy(dst, src) copies min(len(dst), len(src)) elements.
	dst := make([]string, 2)
	n := copy(dst, letters)
	fmt.Println("copy copied", n, "elements:", dst)

	// The slices package replaces many hand-written loops.
	scores := []int{42, 7, 19, 88, 7}
	fmt.Println("Contains 19:", slices.Contains(scores, 19))
	fmt.Println("Index of 88:", slices.Index(scores, 88))
	fmt.Println("Max:", slices.Max(scores), "| Min:", slices.Min(scores))
	slices.Sort(scores)
	fmt.Println("Sorted:", scores)
	idx, found := slices.BinarySearch(scores, 42) // requires sorted input
	fmt.Println("BinarySearch 42:", idx, found)
	scores = slices.Compact(scores) // removes CONSECUTIVE duplicates (sort first)
	fmt.Println("Compact:", scores)
	slices.Reverse(scores)
	fmt.Println("Reversed:", scores)

	// Delete and Insert.
	scores = slices.Delete(scores, 0, 1) // remove index 0
	scores = slices.Insert(scores, 1, 100)
	fmt.Println("after Delete/Insert:", scores)

	// Sort with a custom comparison.
	words := []string{"banana", "Apple", "cherry"}
	slices.SortFunc(words, func(a, b string) int {
		return strings.Compare(strings.ToLower(a), strings.ToLower(b))
	})
	fmt.Println("case-insensitive sort:", words)

	// == is not allowed on slices, so use slices.Equal.
	fmt.Println("Equal:", slices.Equal([]int{1, 2}, []int{1, 2}))

	// The append aliasing gotcha.
	base := make([]int, 3, 10) // spare capacity
	a := append(base, 1)
	b := append(base, 2) // reuses the same backing array and overwrites a's element!
	fmt.Println("aliasing gotcha: a[3] =", a[3], "(expected 1, got 2)", "| b[3] =", b[3])

	// 2D slice: a slice of slices, each row may have a different length.
	grid := make([][]int, 3)
	for i := range grid {
		grid[i] = make([]int, i+1)
		for j := range grid[i] {
			grid[i][j] = i + j
		}
	}
	fmt.Println("triangle grid:", grid)

	// clear zeroes every element (it does not change the length).
	clear(scores)
	fmt.Println("after clear:", scores)
}
