package main

import "fmt"

/*
 * Functions
 * A function can take zero or more arguments.
 * In this example, add takes two parameters of type int.
 * Notice that the type comes after the variable name.
 * When two or more consecutive named function parameters share a type, you can omit the type from all but the last.
 * In this example, we shortened
 * x int, y int
 * to
 * x, y int
 */

func sum(x int, y int) int {
	return x + y
}

func swap(x, y string) (string, string) {
	return y, x
}

func split(sum int) (x, y int) {
	x = sum * 4 / 9
	y = sum - x
	return
}

func main() {
	fmt.Println("Functions in Go")

	fmt.Println("Sum of 2 and 3 is", sum(2, 3))

	a, b := swap("Hello", "World")
	fmt.Println(a, b)

	fmt.Println(split(97))

	// Defer statement
	// A defer statement defers the execution of a function until the surrounding function returns.
	// The deferred call's arguments are evaluated immediately, but the function call is not executed until the surrounding function returns.
	// Deferred function calls are pushed onto a stack. When a function returns, its deferred calls are executed in last-in-first-out order.
	defer fmt.Println("deferred statement")
	fmt.Println("statement after deferred statement")
}
