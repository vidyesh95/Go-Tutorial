// Lesson 03: Functions
//
// A function takes zero or more parameters and may return zero or more results.
//
// Key rules:
//   - The type comes AFTER the name: `func add(x int, y int) int`.
//   - Consecutive parameters of the same type can share it: `x, y int`.
//   - A function may return multiple values. This is how Go reports errors:
//     `value, err := f()`.
//   - Results can be named. A bare `return` then returns their current values.
//   - Arguments are passed BY VALUE (copied). Passing a slice or map copies only
//     its small header, so the callee can still see and change the shared data.
//
// Gotcha: named results with a bare return are fine in tiny functions but hurt
// readability in long ones. Most Go style guides say to prefer explicit returns.
//
// In industry: the idiomatic signature for anything that can fail is
// `func Do(ctx context.Context, in Input) (Output, error)`. Callers must
// handle the error. Lesson 20 covers errors and lesson 21 covers defer in depth.
package main

import (
	"errors"
	"fmt"
)

// sum shows the long form of declaring two parameters of the same type.
func sum(x int, y int) int {
	return x + y
}

// swap shares the type between parameters and returns two values.
func swap(x, y string) (string, string) {
	return y, x
}

// split has named results (x, y). They start at their zero value and a bare
// `return` sends back whatever they hold at that moment.
func split(total int) (x, y int) {
	x = total * 4 / 9
	y = total - x
	return
}

// divide returns a result and an error: the standard Go pattern.
func divide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, errors.New("division by zero")
	}
	return a / b, nil
}

func main() {
	fmt.Println("Functions in Go")

	fmt.Println("Sum of 2 and 3 is", sum(2, 3))

	a, b := swap("Hello", "World")
	fmt.Println("swap:", a, b)

	x, y := split(97)
	fmt.Println("split:", x, y)

	// Always check the error before using the value.
	if q, err := divide(10, 4); err != nil {
		fmt.Println("error:", err)
	} else {
		fmt.Println("10 / 4 =", q)
	}
	if _, err := divide(1, 0); err != nil {
		fmt.Println("error:", err)
	}

	// min and max are built in (Go 1.21+). They work on any ordered type.
	fmt.Println("min:", min(3, 1, 2), "max:", max(3, 1, 2))

	// defer schedules a call to run when the surrounding function returns.
	// Arguments are evaluated NOW, but the call runs LATER, in LIFO order.
	// See lesson 21 for the full story.
	defer fmt.Println("deferred statement (runs last)")
	fmt.Println("statement after deferred statement")
}
