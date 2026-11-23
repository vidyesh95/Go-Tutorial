package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func writeFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "in.csv")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// Hand-checkable input: the expected numbers can be verified with a calculator.
const tiny = `order_id,date,region,product,units,unit_price_cents
1,2026-01-01,europe,widget,2,1000
2,2026-01-02,europe,gadget,1,550
3,2026-01-03,asia,widget,3,100
4,2026-01-04,asia,widget,abc,100
5,2026-01-05,asia,widget
6,2026-01-06,africa,gizmo,1,99
`

func TestProcessorsOnKnownInput(t *testing.T) {
	path := writeFile(t, tiny)
	want := map[string]agg{
		"europe": {Orders: 2, Units: 3, Cents: 2*1000 + 550},
		"asia":   {Orders: 1, Units: 3, Cents: 300},
		"africa": {Orders: 1, Units: 1, Cents: 99},
	}
	for name, fn := range map[string]func() (*Result, error){
		"sequential": func() (*Result, error) { return processSequential(context.Background(), path, 0) },
		"concurrent": func() (*Result, error) { return processConcurrent(context.Background(), path, 3, 2, 0) },
	} {
		t.Run(name, func(t *testing.T) {
			got, err := fn()
			if err != nil {
				t.Fatal(err)
			}
			if got.Rows != 6 || got.Bad != 2 { // "abc" units, and the short row
				t.Errorf("Rows=%d Bad=%d, want 6 and 2", got.Rows, got.Bad)
			}
			for region, w := range want {
				g := got.ByRegion[region]
				g.Flagged = 0 // the fake fraud score is not what this test is about
				if g != w {
					t.Errorf("%s = %+v, want %+v", region, g, w)
				}
			}
		})
	}
}

// The property that matters for any parallelisation: same answer as the simple version, for
// every worker count and batch size (including awkward ones). Run it with -race.
func TestConcurrentMatchesSequential(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sales.csv")
	if err := generateCSV(path, 5_000); err != nil {
		t.Fatal(err)
	}
	want, err := processSequential(context.Background(), path, 3)
	if err != nil {
		t.Fatal(err)
	}
	for _, workers := range []int{1, 2, 7} {
		for _, batch := range []int{1, 64, 10_000} {
			got, err := processConcurrent(context.Background(), path, workers, batch, 3)
			if err != nil {
				t.Fatalf("workers=%d batch=%d: %v", workers, batch, err)
			}
			if !sameResult(want, got) {
				t.Errorf("workers=%d batch=%d: results differ from sequential", workers, batch)
			}
		}
	}
}

func TestCancelledContextStopsPipeline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sales.csv")
	if err := generateCSV(path, 20_000); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := processConcurrent(ctx, path, 4, 100, 50); err == nil {
		t.Error("expected an error from a cancelled context, got nil")
	}
}

func TestMissingFile(t *testing.T) {
	if _, err := processConcurrent(context.Background(), "/no/such/file.csv", 2, 10, 0); err == nil {
		t.Error("expected an error for a missing file")
	}
}

func TestMoney(t *testing.T) {
	for in, want := range map[int64]string{5: "$0.05", 123456: "$1,234.56", 100_000_000: "$1,000,000.00"} {
		if got := money(in); got != want {
			t.Errorf("money(%d) = %q, want %q", in, got, want)
		}
	}
}
