// Package mathx is a normal (public) package: any module may import it.
package mathx

import "fmt"

// Pi is exported: the first letter is uppercase.
const Pi = 3.14159

// secret is unexported: invisible outside package mathx.
var secret = initSecret()

// Package-level variables are initialised first (in dependency order), then init().
func initSecret() int {
	fmt.Println("var init: mathx.secret")
	return 42
}

func init() {
	fmt.Println("init: package mathx")
}

// Counter shows exported type, unexported field: callers cannot reach in and break the invariant.
type Counter struct {
	n int // unexported field
}

// Inc increments the counter and returns the new value.
func (c *Counter) Inc() int {
	c.n++
	return c.n
}

// Square is exported and uses the unexported helper below.
func Square(x int) int { return mul(x, x) }

// mul is unexported: an implementation detail we may change freely.
func mul(a, b int) int { return a * b }

// Secret exposes the hidden value through a function (controlled access).
func Secret() int { return secret }
