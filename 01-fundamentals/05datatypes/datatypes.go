// Lesson 05: Basic data types
//
// Go is statically typed. The basic (built-in) types are:
//
//	bool
//	string
//	int  int8  int16  int32  int64      signed integers
//	uint uint8 uint16 uint32 uint64     unsigned integers (plus uintptr)
//	float32 float64                     floating point
//	complex64 complex128                complex numbers (rare)
//	byte = alias for uint8              one byte of data
//	rune = alias for int32              one Unicode code point
//
// Composite types (arrays, slices, maps, structs, pointers, functions,
// interfaces, channels) are covered in later lessons.
//
// Key rules:
//   - Use `int` for counts, lengths and indexes, and `float64` for decimals.
//     Use sized types only when you need a specific width (binary formats,
//     large arrays where memory matters, hashing).
//   - `int` is 32 or 64 bits depending on the platform (64 on modern machines).
//   - There are NO implicit conversions. int + float64 does not compile; write
//     float64(i) explicitly.
//   - Integer overflow wraps around silently at run time (255 + 1 in a uint8 is 0).
//   - Converting a float to an int truncates toward zero.
//   - A string is an immutable sequence of BYTES (usually UTF-8). Lesson 14 covers
//     strings, bytes and runes in detail.
//
// Gotcha: never use float64 for money. 0.1 + 0.2 != 0.3 in binary floating
// point. Store money as integer minor units (cents) in an int64.
//
// In industry: IDs and counters are int64 or uint64, timestamps use time.Time
// (lesson 25), and money is int64 cents or a decimal library. Choosing int32
// to "save memory" is rarely worth the conversion noise.
package main

import (
	"fmt"
	"math"
)

func main() {
	fmt.Println("Basic data types in Go")

	// %T prints the type, %v prints the value in a default format.
	var username string = "ada"
	var isLoggedIn bool
	var count int = 255
	var small int8 = -128 // int8 holds -128..127
	var u8 uint8 = 255    // uint8 holds 0..255, same as byte
	var f32 float32 = 255.45544
	var f64 float64 = 255.12345678912345
	var r rune = 'G' // a rune literal uses single quotes
	var b byte = 'G' // same number, different type
	var big uint64 = math.MaxUint64
	fmt.Printf("string  : %v (%T)\n", username, username)
	fmt.Printf("bool    : %v (%T) <- zero value\n", isLoggedIn, isLoggedIn)
	fmt.Printf("int     : %v (%T)\n", count, count)
	fmt.Printf("int8    : %v (%T)\n", small, small)
	fmt.Printf("uint8   : %v (%T)\n", u8, u8)
	fmt.Printf("float32 : %v (%T) <- about 7 digits of precision\n", f32, f32)
	fmt.Printf("float64 : %v (%T) <- about 15-16 digits of precision\n", f64, f64)
	fmt.Printf("rune    : %v (%T) as char: %c\n", r, r, r)
	fmt.Printf("byte    : %v (%T) as char: %c\n", b, b, b)
	fmt.Printf("uint64  : %v (%T)\n", big, big)

	// Limits are available as constants in the math package.
	fmt.Println("MaxInt32:", math.MaxInt32, "MinInt64:", math.MinInt64)

	// Type inference: the compiler picks int, float64, string, bool.
	numberOfUsers := 300000
	ratio := 0.75
	fmt.Printf("inferred: %T %T\n", numberOfUsers, ratio)

	// Overflow wraps around silently (no panic, no error).
	var wrap uint8 = 255
	wrap++
	fmt.Println("uint8 255 + 1 =", wrap)

	// Explicit conversion is required between numeric types.
	a := 10
	b2 := 10.5
	fmt.Println("float64(a) + b2 =", float64(a)+b2)
	fmt.Println("a + int(b2)     =", a+int(b2), "(10.5 truncated to 10)")

	// Why float64 is wrong for money.
	// (Constant expressions are exact, so we use variables to see the real behaviour.)
	tenth, fifth := 0.1, 0.2
	fmt.Println("0.1 + 0.2 =", tenth+fifth, "-> use cents (int64) for money")
	cents := int64(1999)
	fmt.Printf("price: $%d.%02d\n", cents/100, cents%100)
}
