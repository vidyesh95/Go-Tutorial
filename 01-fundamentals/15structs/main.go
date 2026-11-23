// Lesson 15: Structs
//
// A struct groups named fields into one value. It is Go's main way to model data
// (there are no classes).
//
//	type User struct {
//	    ID    int
//	    Name  string
//	    Email string
//	}
//
// Key rules:
//   - The zero value of a struct has every field at ITS zero value. Good Go
//     designs make the zero value useful (e.g. sync.Mutex, bytes.Buffer).
//   - Create with a literal. Prefer NAMED fields: User{Name: "Ada"}. Positional
//     literals (User{1, "Ada", ...}) break when fields are added or reordered.
//   - Fields starting with a capital letter are exported. A struct is a
//     package's public API: lowercase fields stay private.
//   - Structs are VALUES: assignment and passing to functions copy them.
//     Use a pointer (*User) to share or mutate (lesson 16).
//   - A struct is comparable with == if all its fields are comparable. A struct
//     containing a slice, map or func is NOT comparable.
//   - Anonymous structs (`struct{ ... }{ ... }`) are handy for one-off data,
//     such as table-driven tests and ad-hoc JSON.
//   - Struct tags are string metadata after a field, read via reflection by
//     encoding/json, database libraries, validators and so on.
//   - Field order affects size because of padding. Group big fields first
//     if memory matters. This rarely matters outside hot paths.
//
// Gotcha: copying a struct that holds a map or slice copies the header, not the
// contents, so both copies still share the underlying data ("shallow" copy).
//
// In industry: structs model API request/response bodies (with `json` tags),
// database rows (`db` tags), configuration and domain entities. Many packages
// use constructor functions (NewServer(...)) when zero values are not enough,
// and "config structs" instead of long parameter lists.
package main

import (
	"fmt"
	"reflect"
)

// Address is a nested struct.
type Address struct {
	City    string
	Country string
}

// User is a typical domain struct.
type User struct {
	ID      int
	Name    string
	Email   string
	Address Address // a nested struct stored by value
	Tags    []string
	active  bool // unexported: only code in this package can access it
}

// Product has struct tags. They do nothing by themselves. Libraries such as
// encoding/json read them with reflection (see lesson 27).
type Product struct {
	ID    int     `json:"id"`
	Name  string  `json:"name"`
	Price float64 `json:"price,omitempty"`
	Note  string  `json:"-"` // never serialised
}

// Point is comparable because both fields are.
type Point struct{ X, Y int }

// NewUser is a constructor function. Go has no constructors; this is a convention.
func NewUser(id int, name, email string) User {
	return User{ID: id, Name: name, Email: email, active: true}
}

func main() {
	fmt.Println("Structs in Go")

	// Zero value: every field is its own zero value.
	var empty User
	fmt.Printf("zero value: %+v\n", empty) // %+v shows field names

	// Named-field literal (preferred): omitted fields are zero.
	ada := User{
		ID:    1,
		Name:  "Ada",
		Email: "ada@example.com",
		Address: Address{
			City:    "London",
			Country: "UK",
		},
	}
	fmt.Printf("ada: %+v\n", ada)
	fmt.Println("city:", ada.Address.City)

	// Positional literal: legal, but fragile. Avoid for structs you own.
	p := Point{3, 4}
	fmt.Printf("point: %v | Go syntax: %#v\n", p, p)

	// Fields are assigned with the dot.
	ada.Email = "ada@lovelace.dev"
	fmt.Println("updated email:", ada.Email)

	// Structs are copied on assignment.
	copyOfAda := ada
	copyOfAda.Name = "Not Ada"
	fmt.Println("original:", ada.Name, "| copy:", copyOfAda.Name)

	// Shallow copy: the slice header is copied, the backing array is shared.
	ada.Tags = []string{"admin"}
	shallow := ada
	shallow.Tags[0] = "CHANGED"
	fmt.Println("shared slice, original now sees:", ada.Tags)

	// Pointer to a struct: fields are accessed with the same dot.
	pu := &ada
	pu.Name = "Ada Lovelace" // same as (*pu).Name
	fmt.Println("via pointer:", ada.Name)

	// Constructor function.
	grace := NewUser(2, "Grace", "grace@example.com")
	fmt.Printf("grace: %+v\n", grace)

	// Comparison: allowed for comparable field types.
	fmt.Println("Point{1,2} == Point{1,2}:", Point{1, 2} == Point{1, 2})
	// User has a slice field, so `ada == grace` would not compile.

	// Structs can be map keys when comparable.
	visits := map[Point]int{{0, 0}: 1, {1, 2}: 5}
	fmt.Println("visits at {1,2}:", visits[Point{1, 2}])

	// Anonymous struct: define and use a type in one place.
	config := struct {
		Host string
		Port int
	}{Host: "localhost", Port: 8080}
	fmt.Printf("anonymous: %+v\n", config)

	// Typical use: a slice of anonymous structs for table-style data.
	cases := []struct {
		input string
		want  int
	}{
		{"a", 1},
		{"bb", 2},
	}
	for _, c := range cases {
		fmt.Println("case:", c.input, c.want, "ok:", len(c.input) == c.want)
	}

	// Tags are just strings, readable through reflection (preview of lesson 27).
	prod := Product{ID: 1, Name: "Pen", Price: 1.5}
	fmt.Printf("product: %+v\n", prod)
	for i := range reflect.TypeFor[Product]().NumField() {
		f := reflect.TypeFor[Product]().Field(i)
		fmt.Printf("  field %-5s json tag = %q\n", f.Name, f.Tag.Get("json"))
	}
}
