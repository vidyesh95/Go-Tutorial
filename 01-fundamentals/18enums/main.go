// Lesson 18: Enums with iota
//
// Go has no `enum` keyword. The idiom is:
//  1. Define a new named type, e.g. `type Status int`.
//  2. Declare typed constants with iota.
//  3. Add a String() method so values print nicely.
//  4. Validate values that come from outside (JSON, DB, user input).
//
// iota:
//   - Inside a const block, iota starts at 0 and increases by 1 on every line.
//   - A line with no expression repeats the previous expression, with iota advanced.
//   - You can use it in expressions: 1 << iota gives bit flags (1, 2, 4, 8...),
//     and `_ = iota` skips a value.
//
// Key rules:
//   - A named type gives some type safety: you cannot pass an untyped variable
//     of another type where a Status is expected. BUT any integer constant
//     converts to Status, so Status(99) compiles. Enums in Go are NOT closed
//     sets; validate values that come from outside.
//   - Make 0 an explicit "unknown" value (or start at iota+1) when the zero
//     value should mean "not set". Otherwise a forgotten field silently becomes
//     the first valid value.
//   - Implement fmt.Stringer (String()) for readable logs. For text formats,
//     implement encoding.TextMarshaler/TextUnmarshaler so JSON uses names.
//   - Do not reorder or insert in the middle of an enum that is persisted as
//     numbers: stored values would change meaning. Append instead.
//
// Gotcha: the compiler does not check that a switch over an enum is exhaustive.
// Add a default case that reports unknown values, or use a linter (exhaustive).
//
// In industry: order statuses, roles, log levels and similar sets are enums.
// Teams often store the string name in the database/JSON (stable and readable)
// and generate String()/parse code with tools such as `stringer`. Bit-flag
// enums model permissions.
package main

import (
	"fmt"
	"strings"
)

// Status is a typed enum. Zero (StatusUnknown) means "not set".
type Status int

const (
	StatusUnknown Status = iota // 0
	StatusPending               // 1
	StatusActive                // 2
	StatusClosed                // 3
)

// statusNames maps each valid value to its name; used by String and Parse.
var statusNames = map[Status]string{
	StatusPending: "pending",
	StatusActive:  "active",
	StatusClosed:  "closed",
}

// String implements fmt.Stringer, so Println prints the name instead of a number.
func (s Status) String() string {
	if name, ok := statusNames[s]; ok {
		return name
	}
	if s == StatusUnknown {
		return "unknown"
	}
	return fmt.Sprintf("Status(%d)", int(s)) // never panic on bad data
}

// Valid reports whether s is one of the known, set values.
func (s Status) Valid() bool {
	_, ok := statusNames[s]
	return ok
}

// ParseStatus converts external text to a Status and validates it.
func ParseStatus(text string) (Status, error) {
	for s, name := range statusNames {
		if strings.EqualFold(name, text) {
			return s, nil
		}
	}
	return StatusUnknown, fmt.Errorf("unknown status %q", text)
}

// Level starts at 1 using iota + 1, so the zero value is not a valid level.
type Level int

const (
	Debug Level = iota + 1
	Info
	Warn
	Error
)

// Permission is a bit-flag enum: values are powers of two so they can be combined.
type Permission uint8

const (
	Read    Permission = 1 << iota // 1
	Write                          // 2
	Execute                        // 4
)

// Has reports whether all bits of q are set in p.
func (p Permission) Has(q Permission) bool { return p&q == q }

func (p Permission) String() string {
	var parts []string
	if p.Has(Read) {
		parts = append(parts, "read")
	}
	if p.Has(Write) {
		parts = append(parts, "write")
	}
	if p.Has(Execute) {
		parts = append(parts, "execute")
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, "|")
}

// Size units: iota in an expression, skipping zero with the blank identifier.
const (
	_  = iota             // skip 0
	KB = 1 << (10 * iota) // 1024
	MB                    // 1048576
	GB                    // 1073741824
)

func main() {
	fmt.Println("Enums in Go")

	s := StatusActive
	fmt.Println("status:", s, "| as number:", int(s))
	fmt.Printf("%%v=%v %%d=%d %%s=%s\n", s, s, s)

	// The zero value is "unknown", so a forgotten field is easy to detect.
	var forgotten Status
	fmt.Println("zero value:", forgotten, "| valid:", forgotten.Valid())

	// Any int converts to Status, so validate!
	bad := Status(99)
	fmt.Println("bad value:", bad, "| valid:", bad.Valid())

	// Parsing external input.
	for _, in := range []string{"Active", "CLOSED", "archived"} {
		st, err := ParseStatus(in)
		if err != nil {
			fmt.Println("parse", in, "-> error:", err)
			continue
		}
		fmt.Println("parse", in, "->", st)
	}

	// switch over an enum, with a default for unexpected values.
	switch s {
	case StatusPending:
		fmt.Println("waiting")
	case StatusActive:
		fmt.Println("in progress")
	case StatusClosed:
		fmt.Println("done")
	default:
		fmt.Println("unexpected status", s)
	}

	fmt.Println("levels:", Debug, Info, Warn, Error)
	fmt.Println("Warn > Info:", Warn > Info, "(integer-based enums can be ordered)")

	// Bit flags.
	p := Read | Write
	fmt.Println("permissions:", p, "| can execute:", p.Has(Execute))
	p |= Execute // add a flag
	fmt.Println("after adding execute:", p)
	p &^= Write // remove a flag
	fmt.Println("after removing write:", p)

	fmt.Println("KB, MB, GB:", KB, MB, GB)
}
