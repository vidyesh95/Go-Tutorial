package main

import "fmt"

// There are 3 types of operators in Go namely Arithmetic, Relational and Logical
func main() {
	x, y := 10, 5

	// There are 5 arithmetic operators in Go namely +, -, *, /, %
	fmt.Println("Arithmetic Operators")
	fmt.Println(x + y)
	fmt.Println(x - y)
	fmt.Println(x * y)
	fmt.Println(x / y)
	fmt.Println(x % y)

	// There are 6 relational operators in Go namely ==, !=, <, >, <=, >=
	fmt.Println("Relational Operators")
	fmt.Println(x == y)
	fmt.Println(x != y)
	fmt.Println(x < y)
	fmt.Println(x > y)
	fmt.Println(x <= y)
	fmt.Println(x >= y)

	// There are 3 logical operators in Go namely &&, ||, !
	fmt.Println("Logical Operators")
	fmt.Println(x < 100 && y > 100)
	fmt.Println(x < 100 || y > 100)
	fmt.Println(!(x < 100 && y > 100))
}
