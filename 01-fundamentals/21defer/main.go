// Lesson 21: defer, panic and recover
//
// defer
//
//	defer f(args)
//
// schedules f to run when the surrounding FUNCTION returns (normally, by
// panic, or by runtime.Goexit).
//
// Key rules:
//   - Deferred calls run in LAST-IN, FIRST-OUT order.
//   - Arguments are evaluated when the defer statement executes, not when the
//     call runs. The function body, however, runs later.
//   - A deferred closure can read and modify NAMED results, which is how
//     cleanup code can set or wrap an error.
//   - defer is scoped to the function, not the block. A defer inside a loop
//     does not run at the end of each iteration; it piles up until the
//     function returns. Move the loop body into a function or call Close
//     explicitly.
//   - Always defer cleanup right after acquiring the resource:
//     open -> check error -> `defer f.Close()`.
//
// panic and recover
//   - panic stops normal execution, runs deferred calls while unwinding the
//     stack, then crashes the program with a stack trace.
//   - recover() stops a panic, but ONLY when called directly from a deferred
//     function. Elsewhere it returns nil.
//   - A panic in a goroutine you did not recover in crashes the whole process.
//
// When is panic acceptable?
//   - Programmer errors and impossible states (an index out of range, a nil
//     map write, "unreachable" switch branches).
//   - Initialisation that cannot proceed: regexp.MustCompile on a constant
//     pattern, template.Must, a missing required config at start-up.
//   - Never for expected failures (bad input, I/O, network). Return an error.
//   - Libraries should not let a panic escape across their API boundary.
//
// In industry: defer is used for Close, Unlock, cancel funcs, and timing.
// HTTP servers recover from panics per request (net/http does this, and
// middleware logs and returns 500) so one bad request does not kill the process.
// Test helpers and worker pools use recover to turn panics into errors.
package main

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"time"
)

// deferOrder shows LIFO order.
func deferOrder() {
	for i := range 3 {
		defer fmt.Println("  deferred", i) // runs after the function body, 2,1,0
	}
	fmt.Println("  body finished")
}

// deferArgs shows that arguments are evaluated immediately.
func deferArgs() {
	x := 1
	defer fmt.Println("  deferred sees x =", x) // x evaluated NOW: 1
	x = 2
	fmt.Println("  body sees x =", x)
}

// deferClosure shows that a closure reads the variable when it RUNS.
func deferClosure() {
	x := 1
	defer func() { fmt.Println("  closure sees x =", x) }() // reads x later: 2
	x = 2
}

// namedResult shows a deferred function changing the returned value.
func namedResult() (result int) {
	defer func() { result *= 2 }()
	return 21 // result is set to 21, then the deferred function doubles it
}

// wrapError adds context to whatever error escapes, using a named error result.
func wrapError(fail bool) (err error) {
	defer func() {
		if err != nil {
			err = fmt.Errorf("wrapError: %w", err)
		}
	}()
	if fail {
		return errors.New("boom")
	}
	return nil
}

// safeDivide turns a runtime panic into an error. Only do this at boundaries.
func safeDivide(a, b int) (result int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered: %v", r)
		}
	}()
	return a / b, nil // integer division by zero panics
}

// readFileSafely shows the canonical resource pattern.
func readFileSafely(name string) error {
	f, err := os.Open(name)
	if err != nil {
		return err // nothing to close yet
	}
	defer f.Close() // runs on every return path below
	_, err = f.Stat()
	return err
}

// counter shows defer with a mutex: unlock on every return path, even a panic.
type counter struct {
	mu sync.Mutex
	n  int
}

func (c *counter) Inc() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
	return c.n
}

// timed is a common deferred helper: call it with defer timed("x")().
func timed(label string) func() {
	start := time.Now()
	return func() {
		fmt.Printf("  %s took less than 1s: %t\n", label, time.Since(start) < time.Second)
	}
}

// processAll avoids the defer-in-loop trap by putting each iteration in a function.
func processAll(names []string) {
	for _, n := range names {
		func() {
			fmt.Println("  open", n)
			defer fmt.Println("  close", n) // runs at the end of THIS iteration
		}()
	}
}

func main() {
	fmt.Println("defer, panic and recover")

	fmt.Println("defer order:")
	deferOrder()

	fmt.Println("arguments evaluated immediately:")
	deferArgs()

	fmt.Println("closure reads the variable later:")
	deferClosure()

	fmt.Println("named result doubled by defer:", namedResult())

	fmt.Println("wrapError(true):", wrapError(true))
	fmt.Println("wrapError(false):", wrapError(false))

	q, err := safeDivide(10, 2)
	fmt.Println("safeDivide(10, 2):", q, err)
	q, err = safeDivide(1, 0)
	fmt.Println("safeDivide(1, 0):", q, err)

	fmt.Println("readFileSafely:", readFileSafely("/not/a/file") != nil)

	var c counter
	c.Inc()
	fmt.Println("counter:", c.Inc())

	func() {
		defer timed("quick work")()
	}()

	fmt.Println("defer inside a per-iteration function:")
	processAll([]string{"a", "b"})

	// recover returns whatever was passed to panic.
	func() {
		defer func() {
			r := recover()
			fmt.Printf("recovered value: %v (type %T)\n", r, r)
		}()
		panic("something impossible happened")
	}()

	// A runtime error is a panic too; the recovered value is an error.
	func() {
		defer func() {
			if r, ok := recover().(error); ok {
				fmt.Println("runtime error recovered:", r)
			}
		}()
		var m map[string]int
		m["x"] = 1 // writing to a nil map panics
	}()

	fmt.Println("program continues normally after recovered panics")
}
