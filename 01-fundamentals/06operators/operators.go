// Lesson 06: Operators
//
// Go's operators fall into a few groups:
//
//	Arithmetic   + - * / % (and ++ -- as STATEMENTS, not expressions)
//	Comparison   == != < > <= >=   (result is always a bool)
//	Logical      && || !           (&& and || short-circuit)
//	Bitwise      & | ^ &^ << >>
//	Assignment   = += -= *= /= %= &= |= ^= <<= >>=
//
// Key rules:
//   - Both operands must have the SAME type. int + int64 does not compile.
//   - Integer division truncates toward zero: 7 / 2 == 3 and -7 / 2 == -3.
//     The result of % takes the sign of the left operand: -7 % 2 == -1.
//   - Dividing an integer by zero panics at run time. Floats give +Inf or NaN.
//   - `i++` is a statement. `x = i++` and `++i` do not exist in Go.
//   - && and || short-circuit: the right side runs only if it is needed.
//     That is why `p != nil && p.Ready` is a safe nil check.
//   - There is no ternary operator (`a ? b : c`). Use if/else.
//
// In industry: bit flags (permissions, feature masks) use the bitwise
// operators; most business code uses only arithmetic, comparisons and
// logical operators. Short-circuit guards such as `len(xs) > 0 && xs[0] == v`
// are everywhere.
package main

import "fmt"

func main() {
	x, y := 10, 5

	fmt.Println("Arithmetic operators")
	fmt.Println("x + y =", x+y)
	fmt.Println("x - y =", x-y)
	fmt.Println("x * y =", x*y)
	fmt.Println("x / y =", x/y)
	fmt.Println("x % y =", x%y)
	fmt.Println("7 / 2 =", 7/2, "(integer division truncates)")
	fmt.Println("-7 / 2 =", -7/2, "and -7 % 2 =", -7%2)
	fmt.Println("7.0 / 2 =", 7.0/2, "(untyped float constant)")

	fmt.Println("Comparison operators")
	fmt.Println("x == y:", x == y)
	fmt.Println("x != y:", x != y)
	fmt.Println("x < y: ", x < y)
	fmt.Println("x > y: ", x > y)
	fmt.Println("x <= y:", x <= y)
	fmt.Println("x >= y:", x >= y)

	fmt.Println("Logical operators")
	fmt.Println("x < 100 && y > 100:", x < 100 && y > 100)
	fmt.Println("x < 100 || y > 100:", x < 100 || y > 100)
	fmt.Println("!(x < 100):", !(x < 100))

	// Short-circuit: the second operand is not evaluated when the first decides.
	var xs []int
	fmt.Println("safe guard:", len(xs) > 0 && xs[0] == 1) // xs[0] never runs

	fmt.Println("Bitwise operators")
	a, b := 0b1100, 0b1010 // binary literals
	fmt.Printf("a & b  = %04b (AND)\n", a&b)
	fmt.Printf("a | b  = %04b (OR)\n", a|b)
	fmt.Printf("a ^ b  = %04b (XOR)\n", a^b)
	fmt.Printf("a &^ b = %04b (AND NOT: clear bits of b)\n", a&^b)
	fmt.Printf("1 << 3 = %d (shift left multiplies by 2^3)\n", 1<<3)

	// Typical use: permission flags.
	const (
		read  = 1 << iota // 1
		write             // 2
		exec              // 4
	)
	perms := read | write
	fmt.Println("can write:", perms&write != 0, "| can exec:", perms&exec != 0)

	fmt.Println("Assignment operators")
	n := 10
	n += 5
	n *= 2
	n++ // a statement, not an expression
	fmt.Println("n:", n)
}
