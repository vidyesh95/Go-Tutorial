// Lesson 19: Embedding and composition
//
// Go has no inheritance. It favours COMPOSITION and offers embedding as
// syntactic sugar: put a type in a struct WITHOUT a field name.
//
//	type Dog struct {
//	    Animal        // embedded: Dog "has an" Animal
//	    Breed string
//	}
//
// Dog gets Animal's fields and methods PROMOTED: dog.Name and dog.Speak()
// work directly. Under the hood the field is still called Animal and you may
// write dog.Animal.Name.
//
// Key rules:
//   - Embedding is NOT inheritance. There is no virtual dispatch: if
//     Animal.Describe() calls a.Speak(), it always calls Animal's Speak, never
//     Dog's override. The embedded value does not know it is embedded.
//   - The outer type can "shadow" a promoted method by declaring its own method
//     with the same name. Call the inner one explicitly: dog.Animal.Speak().
//   - Method sets: embedding T gives the outer VALUE T's value-receiver methods
//     (and *Outer gets T's pointer methods too). Embedding *T gives both kinds
//     to the outer value as well.
//   - If two embedded types promote the same name at the same depth, using it
//     unqualified is a compile error (ambiguous selector).
//   - Interface embedding composes interfaces: io.ReadWriter is
//     `interface { Reader; Writer }`.
//   - Embedding an interface in a struct is a handy way to wrap and override
//     just one method (decorators, test doubles).
//   - Embedding exposes the embedded type's exported API on YOUR type, which
//     makes it part of your public API. Think before embedding in exported types.
//
// Gotcha: embedding sync.Mutex in an exported struct exposes Lock/Unlock to all
// users of the type. Prefer a named, unexported field: `mu sync.Mutex`.
//
// In industry: embedding shares small bits of behaviour (a base model with ID
// and timestamps, an embedded interface in middleware/decorators). When in
// doubt, use a named field and write the forwarding method by hand: it is more
// explicit and does not widen your API.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
)

// Base is a reusable chunk of data and behaviour.
type Base struct {
	ID        int
	CreatedAt time.Time
}

// Age is promoted to any type that embeds Base.
func (b Base) Age(now time.Time) time.Duration { return now.Sub(b.CreatedAt) }

// Animal is embedded in Dog.
type Animal struct {
	Name string
}

func (a Animal) Speak() string    { return a.Name + " makes a sound" }
func (a Animal) Describe() string { return "I am " + a.Name + " and " + a.Speak() }

// Dog embeds Animal and overrides Speak.
type Dog struct {
	Animal
	Breed string
}

func (d Dog) Speak() string { return d.Name + " barks" }

// User embeds Base. Base's fields and methods are promoted.
type User struct {
	Base
	Email string
}

// Logger is a small interface for the decorator below.
type Logger interface {
	Log(msg string)
}

type consoleLogger struct{}

func (consoleLogger) Log(msg string) { fmt.Println("LOG:", msg) }

// prefixLogger embeds the Logger interface and overrides only Log.
type prefixLogger struct {
	Logger
	prefix string
}

func (p prefixLogger) Log(msg string) { p.Logger.Log(p.prefix + msg) }

// A and B both have Hello, so AB.Hello() is ambiguous and must be qualified.
type A struct{}
type B struct{}

func (A) Hello() string { return "A" }
func (B) Hello() string { return "B" }

type AB struct {
	A
	B
}

// countingWriter wraps any io.Writer and counts bytes: composition via a field.
type countingWriter struct {
	w io.Writer
	n int
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += n
	return n, err
}

func main() {
	fmt.Println("Embedding in Go")

	// Promoted fields and methods.
	u := User{Base: Base{ID: 7, CreatedAt: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)}, Email: "a@b.dev"}
	fmt.Println("promoted field u.ID:", u.ID, "| explicit u.Base.ID:", u.Base.ID)
	fmt.Println("promoted method Age:", u.Age(time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)))

	// Overriding (shadowing).
	d := Dog{Animal: Animal{Name: "Rex"}, Breed: "Lab"}
	fmt.Println("Dog.Speak (override):", d.Speak())
	fmt.Println("Dog.Animal.Speak (inner):", d.Animal.Speak())

	// NOT virtual dispatch: Describe belongs to Animal and calls Animal.Speak.
	fmt.Println("Describe:", d.Describe(), "<- still the Animal version!")

	// Ambiguity: both.Hello() would not compile; qualify it.
	both := AB{}
	fmt.Println("AB:", both.A.Hello(), both.B.Hello())

	// Decorating an interface with embedding.
	var l Logger = prefixLogger{Logger: consoleLogger{}, prefix: "[app] "}
	l.Log("started")

	// Composition around the io.Writer interface.
	cw := &countingWriter{w: os.Stdout}
	fmt.Fprintln(cw, "written through the counter")
	fmt.Println("bytes counted:", cw.n)

	// Interface embedding in the standard library: io.ReadWriter = Reader + Writer.
	var rw io.ReadWriter = struct {
		io.Reader
		io.Writer
	}{strings.NewReader("data"), io.Discard}
	buf := make([]byte, 4)
	n, _ := rw.Read(buf)
	fmt.Println("read through io.ReadWriter:", string(buf[:n]))
}
