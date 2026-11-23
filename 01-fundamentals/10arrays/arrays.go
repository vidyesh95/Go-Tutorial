// Lesson 10: Arrays
//
// An array is a FIXED-length sequence of elements of one type.
// The length is part of the type: [3]int and [4]int are different types.
//
//	var a [5]int              // five zeros
//	b := [3]string{"x", "y", "z"}
//	c := [...]int{1, 2, 3}    // compiler counts: [3]int
//
// Key rules:
//   - Arrays are VALUES. Assigning or passing an array copies every element.
//   - Arrays of the same type can be compared with == (all elements are compared).
//   - Indexing out of range panics at run time (or fails to compile if the
//     index is a constant).
//   - len(a) is a compile-time constant for arrays.
//   - Slicing an array (a[1:3]) gives a SLICE that shares the array's memory.
//
// Gotcha: because arrays are copied on assignment, writing to the copy does
// not change the original. That surprises people coming from other languages.
//
// In industry: you rarely use arrays directly; slices (lesson 11) are the
// workhorse. Arrays appear when the size is genuinely fixed and part of the
// meaning: [16]byte for a UUID, [32]byte for a SHA-256 hash, [3]float64 for a
// vector. Because they are comparable, they can also be map keys.
package main

import "fmt"

func main() {
	fmt.Println("Arrays in Go")

	// Zero-valued array, then assign by index.
	var even [5]int
	for i := range len(even) {
		even[i] = i * 2
	}
	fmt.Println("even:", even)

	// Array literal.
	odd := [5]int{1, 3, 5, 7, 9}
	fmt.Println("odd:", odd, "len:", len(odd))

	// [...] lets the compiler count the elements.
	names := [...]string{"Yash", "Raj", "Rohit", "Sachin"}
	fmt.Printf("names: %v len=%d type=%T\n", names, len(names), names)

	// Indexed literal: unlisted elements are zero.
	sparse := [5]int{1: 10, 3: 30}
	fmt.Println("sparse:", sparse)

	fmt.Println("first:", names[0], "| last:", names[len(names)-1])

	// Arrays are values: b is an independent copy.
	a := [3]int{1, 2, 3}
	b := a
	b[0] = 99
	fmt.Println("a:", a, "b:", b, "(a unchanged)")

	// == compares every element.
	fmt.Println("a == [3]int{1,2,3}:", a == [3]int{1, 2, 3})

	// Slicing an array produces a slice that SHARES the array's memory.
	s := names[1:3] // elements at index 1 and 2
	fmt.Println("names[1:3]:", s, "len:", len(s), "cap:", cap(s))
	s[0] = "CHANGED"
	fmt.Println("original array sees the change:", names)

	// Common slice expressions.
	fmt.Println("names[1:]:", names[1:])
	fmt.Println("names[:2]:", names[:2])
	fmt.Println("names[:]:", names[:]) // a slice covering the whole array

	// Fixed-size arrays are handy for things like hashes or IDs.
	var id [4]byte = [4]byte{0xde, 0xad, 0xbe, 0xef}
	fmt.Printf("id as hex: %x\n", id)
}
