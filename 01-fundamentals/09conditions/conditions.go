// Lesson 09: Conditions (if and switch)
//
// if:
//
//	if cond { } else if cond { } else { }
//
// An `if` can start with a short statement whose variables are scoped to the
// whole if/else chain. This is the idiomatic way to handle errors:
//
//	if v, err := f(); err != nil { ... } else { use(v) }
//
// switch:
//   - Cases do NOT fall through. Each case ends automatically (no `break` needed).
//     Use the `fallthrough` keyword in the rare case you want the next one to run.
//   - A case can list several values: `case 1, 2, 3:`.
//   - A switch with no tag (`switch { case x > 5: ... }`) is a cleaner if/else-if chain.
//   - Cases are evaluated top to bottom; the first match wins.
//   - `default` runs if nothing matches. It may appear anywhere but is conventionally last.
//   - There is also a type switch (`switch v := x.(type)`), see lesson 17.
//
// Gotcha: the condition must be a bool. `if n { }` with an int does not compile.
// There are no truthy or falsy values in Go. Also, there is no ternary operator.
//
// In industry: the "happy path stays left" style is the norm. Handle the
// error or the edge case first and return early, instead of nesting the
// success logic inside else blocks. Tagless switches replace long else-if chains.
package main

import (
	"fmt"
	"strconv"
)

// classify uses a tagless switch as a tidy if/else-if chain.
func classify(age int) string {
	switch {
	case age < 0:
		return "invalid"
	case age < 13:
		return "child"
	case age < 18:
		return "teen"
	default:
		return "adult"
	}
}

func main() {
	fmt.Println("Conditions in Go")
	age := 20

	if age >= 18 {
		fmt.Println("You can vote!")
	} else {
		fmt.Println("You cannot vote!")
	}

	// if with a short statement. n and err exist only inside the if/else.
	if n, err := strconv.Atoi("42x"); err != nil {
		fmt.Println("not a number:", err)
	} else {
		fmt.Println("number:", n)
	}

	// switch on a value. No fallthrough, so no break statements needed.
	switch age {
	case 16:
		fmt.Println("You can drive!")
	case 18:
		fmt.Println("You can vote!")
	case 21:
		fmt.Println("You can drink!")
	default:
		fmt.Println("You can have fun!")
	}

	// Several values in one case.
	day := "Sat"
	switch day {
	case "Sat", "Sun":
		fmt.Println(day, "is weekend")
	default:
		fmt.Println(day, "is a weekday")
	}

	// Tagless switch.
	for _, a := range []int{-1, 5, 15, 30} {
		fmt.Println("age", a, "is", classify(a))
	}

	// fallthrough is explicit and rarely used.
	switch 1 {
	case 1:
		fmt.Println("case 1")
		fallthrough
	case 2:
		fmt.Println("case 2 (reached by fallthrough)")
	case 3:
		fmt.Println("case 3 (not reached)")
	}
}
