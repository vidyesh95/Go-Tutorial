// Lesson 13: range
//
// `range` iterates over many kinds of values in a `for` loop:
//
//	for i, v := range slice   // index, element (a COPY of the element)
//	for i, v := range array
//	for k, v := range map     // key, value, in random order
//	for i, r := range string  // byte index, rune (a Unicode code point)
//	for v := range channel    // until the channel is closed
//	for i := range 10         // 0..9 (Go 1.22+)
//	for v := range seq        // a function iterator (Go 1.23+, lesson 24)
//
// Key rules:
//   - Drop what you do not need: `for i := range s` (index only),
//     `for _, v := range s` (value only), `for range s` (neither).
//   - The element `v` is a COPY. Changing v does not change the slice; assign
//     through the index (`s[i] = ...`) or use a pointer/struct field.
//   - For a slice, the range expression is evaluated ONCE, so the loop runs the
//     original length even if you append inside the loop.
//   - Strings: range decodes UTF-8. The index jumps by the width of each rune
//     (1 to 4 bytes). Invalid bytes yield U+FFFD.
//   - Maps: order is random on purpose. Deleting entries during iteration is
//     allowed and safe.
//   - Since Go 1.22 each iteration has its own i and v variables.
//
// Gotcha: ranging over a slice of structs and taking &v used to give every
// pointer the same address. With per-iteration variables (Go 1.22+) this bug
// is gone, but taking &s[i] is still the clearer way to point at the element.
//
// In industry: range is everywhere. Sorting keys before iterating a map gives
// deterministic output, and the iterator forms (maps.Keys, slices.Values,
// strings.SplitSeq) let you chain such loops without building temporary slices.
package main

import (
	"fmt"
	"maps"
	"slices"
)

type user struct {
	name   string
	active bool
}

func main() {
	fmt.Println("range in Go")

	// Slice: index and value.
	nums := []int{2, 3, 4}
	sum := 0
	for i, n := range nums {
		fmt.Println("index:", i, "value:", n)
		sum += n
	}
	fmt.Println("sum:", sum)

	// Index only, and value only.
	for i := range nums {
		fmt.Print(i, " ")
	}
	fmt.Println("<- index only")
	for _, n := range nums {
		fmt.Print(n, " ")
	}
	fmt.Println("<- value only")

	// The value is a COPY. To modify the element, use the index.
	for _, n := range nums {
		n *= 10 // changes only the copy
	}
	fmt.Println("after modifying copies:", nums)
	for i := range nums {
		nums[i] *= 10
	}
	fmt.Println("after modifying through the index:", nums)

	// Same trap with structs: use the index (or a slice of pointers).
	users := []user{{"ann", false}, {"bob", false}}
	for _, u := range users {
		u.active = true // copy only
	}
	fmt.Println("copy modification:", users)
	for i := range users {
		users[i].active = true
	}
	fmt.Println("index modification:", users)

	// Map: random order, so sort the keys for stable output.
	kvs := map[string]string{"b": "banana", "a": "apple", "c": "cherry"}
	for _, k := range slices.Sorted(maps.Keys(kvs)) {
		fmt.Println("key:", k, "value:", kvs[k])
	}

	// String: byte index and rune. "héllo" has a 2-byte character.
	for i, r := range "héllo" {
		fmt.Printf("byte index %d: %c (U+%04X)\n", i, r, r)
	}

	// Range over an int (Go 1.22+).
	for i := range 3 {
		fmt.Print("i=", i, " ")
	}
	fmt.Println()

	// Range over a channel: ends when the channel is closed.
	ch := make(chan string, 2)
	ch <- "one"
	ch <- "two"
	close(ch)
	for msg := range ch {
		fmt.Println("received:", msg)
	}

	// The range expression is evaluated once: appending does not extend the loop.
	grow := []int{1, 2, 3}
	for _, v := range grow {
		grow = append(grow, v*10)
	}
	fmt.Println("grow:", grow)

	// slices.All and slices.Backward give iterators (Go 1.23+).
	for i, v := range slices.Backward([]string{"x", "y", "z"}) {
		fmt.Print(i, "=", v, " ")
	}
	fmt.Println("<- backward")
}
