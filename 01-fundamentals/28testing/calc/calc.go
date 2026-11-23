// Package calc is a tiny library that exists to be tested.
//
// Lesson 28 shows how Go's built-in `testing` package works. The tests live in
// calc_test.go, right next to this file.
package calc

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// ErrDivideByZero is a sentinel error callers can match with errors.Is.
var ErrDivideByZero = errors.New("divide by zero")

// Add returns a + b.
func Add(a, b int) int { return a + b }

// Divide returns a / b, or ErrDivideByZero when b is 0.
func Divide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, ErrDivideByZero
	}
	return a / b, nil
}

// Eval evaluates a simple "<number> <op> <number>" expression such as "3 + 4".
// Supported operators: + - * /
func Eval(expr string) (float64, error) {
	fields := strings.Fields(expr)
	if len(fields) != 3 {
		return 0, fmt.Errorf("want \"<a> <op> <b>\", got %q", expr)
	}
	a, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, fmt.Errorf("left operand: %w", err)
	}
	b, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return 0, fmt.Errorf("right operand: %w", err)
	}
	switch fields[1] {
	case "+":
		return a + b, nil
	case "-":
		return a - b, nil
	case "*":
		return a * b, nil
	case "/":
		return Divide(a, b)
	default:
		return 0, fmt.Errorf("unknown operator %q", fields[1])
	}
}

// SumSlow adds the numbers but checks ctx between steps, so a caller can cancel it.
func SumSlow(ctx context.Context, nums []int) (int, error) {
	total := 0
	for _, n := range nums {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		total += n
	}
	return total, nil
}

// SaveResult writes v to a file. It exists to show t.TempDir in tests.
func SaveResult(path string, v float64) error {
	return os.WriteFile(path, []byte(strconv.FormatFloat(v, 'g', -1, 64)), 0o644)
}

// LoadResult reads back a value written by SaveResult.
func LoadResult(path string) (float64, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseFloat(string(b), 64)
}
