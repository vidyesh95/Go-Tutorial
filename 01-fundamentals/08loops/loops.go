// Lesson 08: Loops
//
// `for` is Go's ONLY loop keyword. It has several shapes:
//
//	for i := 0; i < n; i++ { }   classic three-part loop
//	for i := range n { }         count 0..n-1 (Go 1.22+), the modern way
//	for cond { }                 "while" loop
//	for { }                      infinite loop, leave with break or return
//	for i, v := range xs { }     iterate a slice, array, map, string, channel or func
//
// Key rules:
//   - `break` leaves the innermost loop, `continue` jumps to its next iteration.
//   - A LABEL lets break/continue target an outer loop.
//   - Since Go 1.22 every iteration gets its OWN copy of the loop variable.
//     Closures or goroutines that capture it see the value of their own
//     iteration. The old `i := i` workaround is no longer needed.
//   - range is covered in more depth in lesson 13.
//
// Gotcha: modifying the loop variable inside the body of a three-part loop
// affects the loop (the per-iteration copy is copied back before the post
// statement). Avoid it.
//
// In industry: `for range n` and `for ... range` over slices dominate. Infinite
// loops with `select` power workers, and labelled breaks are used sparingly,
// usually to exit a `select`-in-`for` loop. If you need a label often, extract
// the inner loop into a function and `return`.
package main

import "fmt"

func main() {
	fmt.Println("Classic for loop")
	for i := 1; i <= 3; i++ {
		fmt.Println(i)
	}

	// Go 1.22+: range over an integer. i takes 0, 1, 2.
	fmt.Println("Range over int")
	for i := range 3 {
		fmt.Println(i)
	}

	// Repeat N times when you do not need the counter.
	fmt.Println("Repeat without a variable")
	for range 2 {
		fmt.Println("hello")
	}

	// "while" loop: just a condition.
	fmt.Println("While-style loop")
	n := 1
	for n <= 8 {
		fmt.Println(n)
		n *= 2
	}

	// Infinite loop with an explicit exit.
	fmt.Println("Loop with break")
	attempts := 0
	for {
		attempts++
		if attempts == 3 {
			break
		}
	}
	fmt.Println("stopped after", attempts, "attempts")

	fmt.Println("Continue (odd numbers only)")
	for i := range 6 {
		if i%2 == 0 {
			continue
		}
		fmt.Println(i)
	}

	// A plain break only leaves the INNER loop.
	fmt.Println("Nested loop with break (inner loop only)")
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			if i == 2 && j == 2 {
				break // outer loop continues with i = 3
			}
			fmt.Println(i, j)
		}
	}

	// A label breaks the OUTER loop.
	fmt.Println("Labelled break (leaves both loops)")
Outer:
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			if i == 2 && j == 2 {
				break Outer
			}
			fmt.Println(i, j)
		}
	}

	// A labelled continue skips to the next iteration of the OUTER loop.
	fmt.Println("Labelled continue")
Rows:
	for i := 1; i <= 3; i++ {
		for j := 1; j <= 3; j++ {
			if j == 2 {
				continue Rows
			}
			fmt.Println(i, j)
		}
	}

	// Per-iteration loop variables (Go 1.22+): each closure sees its own i.
	fmt.Println("Per-iteration loop variable")
	var printers []func()
	for i := range 3 {
		printers = append(printers, func() { fmt.Println("closure sees", i) })
	}
	for _, p := range printers {
		p()
	}

	// range over a slice (see lesson 13 for more).
	fmt.Println("Range over a slice")
	s := []string{"a", "b", "c"}
	for i, v := range s {
		fmt.Println(i, v)
	}
}
