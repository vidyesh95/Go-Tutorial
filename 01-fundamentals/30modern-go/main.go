// Lesson 30: A tour of modern Go (1.22 to 1.27)
//
// Many tutorials and Stack Overflow answers are years old. This lesson collects
// newer features you should know, each in its own small function with the Go version
// that introduced it. Read the function, then compare the output.
//
//	1.22  range over int; per-iteration loop variables
//	1.23  iterators (iter.Seq) and range-over-func; unique package
//	1.21  min / max / clear builtins (a bit older, but still missed by many guides)
//	1.24  crypto/rand.Text
//	1.25  sync.WaitGroup.Go
//	1.26  new(expr); errors.AsType; slog.NewMultiHandler
//	1.27  uuid package (uuid.NewV7); strings.CutLast; url.Values.Clone
//
// Rules of thumb:
//   - The `go` line in go.mod (here go 1.27.1) decides which language features compile.
//     Library authors who support older Go versions must not use newer features.
//   - Prefer the standard library over a dependency when it now covers the need
//     (UUIDs, multi-sink logging, typed error matching).
//
// In industry: these features are what modern Go code reviews look like. UUIDv7 primary
// keys keep database indexes compact and insert-friendly (see uuidV7 below), errors.AsType
// removes the "declare a target variable" boilerplate, and slog (with a multi handler)
// is the standard structured logging stack for services.
package main

import (
	"crypto/rand"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"net/url"
	"os"
	"slices"
	"strings"
	"sync"
	"unique"
	"uuid"
)

func main() {
	rangeOverInt()
	loopVarFix()
	minMaxClear()
	iterators()
	randText()
	uniqueHandles()
	waitGroupGo()
	newExpr()
	asType()
	multiHandler()
	uuidV7()
	cutLast()
	cloneValues()
}

// Go 1.22: `for i := range n` replaces the three-clause loop for simple counting.
func rangeOverInt() {
	fmt.Println("--- range over int (1.22) ---")
	for i := range 3 {
		fmt.Print(i, " ")
	}
	fmt.Println()
}

// Go 1.22: each iteration gets its OWN copy of the loop variable. Before 1.22 all
// closures below would capture the same variable and print 3 3 3.
func loopVarFix() {
	fmt.Println("\n--- loop variable per iteration (1.22) ---")
	var funcs []func() int
	for i := range 3 {
		funcs = append(funcs, func() int { return i })
	}
	for _, f := range funcs {
		fmt.Print(f(), " ")
	}
	fmt.Println("(was 3 3 3 before Go 1.22)")
}

// Go 1.21: min, max and clear are builtins (no import needed).
func minMaxClear() {
	fmt.Println("\n--- min / max / clear (1.21) ---")
	fmt.Println("min(3, 1, 2) =", min(3, 1, 2), "| max(2.5, 1) =", max(2.5, 1))
	m := map[string]int{"a": 1, "b": 2}
	clear(m) // deletes all keys from a map (zeroes elements of a slice)
	fmt.Println("after clear, len =", len(m))
}

// Go 1.23: range over functions (iterators), plus slices/maps helpers that return them.
func iterators() {
	fmt.Println("\n--- iterators (1.23) ---")
	countdown := func(from int) iter.Seq[int] {
		return func(yield func(int) bool) {
			for i := from; i > 0; i-- {
				if !yield(i) {
					return
				}
			}
		}
	}
	fmt.Println("countdown:", slices.Collect(countdown(3)))
	fmt.Println("see lesson 24 for the full story")
}

// Go 1.24: crypto/rand.Text returns a cryptographically secure random string
// (base32 alphabet, 26 characters = 128 bits). Handy for tokens and request IDs.
func randText() {
	fmt.Println("\n--- crypto/rand.Text (1.24) ---")
	token := rand.Text()
	fmt.Println("token length:", len(token), "(random, so we do not print it)")
}

// Go 1.23: unique.Make interns (deduplicates) comparable values. Two handles are
// equal exactly when the values are equal, and comparing handles is a cheap pointer
// comparison. Good for repeated strings such as hostnames or label values.
func uniqueHandles() {
	fmt.Println("\n--- unique.Make (1.23) ---")
	a := unique.Make("api.example.com")
	b := unique.Make(strings.Clone("api.example.com")) // different backing memory
	fmt.Println("handles equal:", a == b, "| value:", a.Value())
}

// Go 1.25: WaitGroup.Go starts the goroutine AND does Add(1)/Done() for you.
// It removes the classic bug of forgetting wg.Add or wg.Done.
func waitGroupGo() {
	fmt.Println("\n--- sync.WaitGroup.Go (1.25) ---")
	var wg sync.WaitGroup
	var mu sync.Mutex
	total := 0
	for i := range 5 {
		wg.Go(func() {
			mu.Lock()
			defer mu.Unlock()
			total += i
		})
	}
	wg.Wait()
	fmt.Println("sum of 0..4 =", total)
}

// Go 1.26: new accepts an expression, so you can take a pointer to a literal in one step.
// Great for optional JSON/DB fields (*int, *string) without a throwaway variable.
func newExpr() {
	fmt.Println("\n--- new(expr) (1.26) ---")
	type Profile struct {
		Name string
		Age  *int // nil means "unknown"
	}
	p := Profile{Name: "Ada", Age: new(36)} // before 1.26: age := 36; Age: &age
	name := new("Grace")
	fmt.Println(p.Name, *p.Age, *name)
}

type NotFoundError struct{ ID int }

func (e *NotFoundError) Error() string { return fmt.Sprintf("id %d not found", e.ID) }

// Go 1.26: errors.AsType[E] is a generic, type-safe errors.As. No target variable,
// no pointer-to-pointer, and the compiler checks that E is an error type.
func asType() {
	fmt.Println("\n--- errors.AsType (1.26) ---")
	err := fmt.Errorf("loading user: %w", &NotFoundError{ID: 7})

	// Old style:
	//   var nf *NotFoundError
	//   if errors.As(err, &nf) { ... }
	if nf, ok := errors.AsType[*NotFoundError](err); ok {
		fmt.Println("not found id:", nf.ID)
	}
}

// Go 1.26: slog.NewMultiHandler fans one log record out to several handlers,
// e.g. human-readable text to stdout and JSON to a file/collector.
func multiHandler() {
	fmt.Println("\n--- slog.NewMultiHandler (1.26) ---")
	// Remove the time attribute so the output is stable for this demo.
	noTime := func(groups []string, a slog.Attr) slog.Attr {
		if a.Key == slog.TimeKey && len(groups) == 0 {
			return slog.Attr{}
		}
		return a
	}
	opts := &slog.HandlerOptions{ReplaceAttr: noTime}
	h := slog.NewMultiHandler(
		slog.NewTextHandler(os.Stdout, opts),
		slog.NewJSONHandler(os.Stdout, opts),
	)
	slog.New(h).Info("order created", "order_id", 123)
}

// Go 1.27: the standard library has a uuid package.
//   - uuid.New()    default, suitable for most uses
//   - uuid.NewV4()  fully random
//   - uuid.NewV7()  48-bit millisecond timestamp prefix + random bits, so values sort by
//     creation time. As database primary keys this keeps B-tree inserts mostly
//     append-only (random v4 keys scatter inserts and fragment indexes).
func uuidV7() {
	fmt.Println("\n--- uuid.NewV7 (1.27) ---")
	a := uuid.NewV7()
	b := uuid.NewV7()
	fmt.Println("v7 looks like:", a.String()[:8]+"-...", "| length:", len(a.String()))
	fmt.Println("later id sorts after earlier:", a.Compare(b) < 0)

	parsed, err := uuid.Parse(a.String())
	fmt.Println("round trip ok:", err == nil && parsed == a) // UUID is comparable with ==
	fmt.Println("Nil():", uuid.Nil())
}

// Go 1.27: strings.CutLast splits around the LAST separator (strings.Cut uses the first).
// Perfect for file extensions or "host:port" with IPv6.
func cutLast() {
	fmt.Println("\n--- strings.CutLast (1.27) ---")
	name, ext, found := strings.CutLast("archive.tar.gz", ".")
	fmt.Println(name, "|", ext, "|", found)
	before, _, found := strings.Cut("archive.tar.gz", ".") // first separator, for contrast
	fmt.Println(before, found)
}

// Go 1.27: url.Values.Clone makes a deep copy, so changing the copy cannot change the
// original query (previously you had to copy every slice by hand).
func cloneValues() {
	fmt.Println("\n--- url.Values.Clone (1.27) ---")
	q := url.Values{"tag": {"go", "backend"}}
	c := q.Clone()
	c.Add("tag", "uuid")
	c["tag"][0] = "changed"
	fmt.Println("original:", q.Encode())
	fmt.Println("clone   :", c.Encode())
}
