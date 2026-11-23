// Use case 07: profiling and benchmarking (finding out WHY Go code is slow).
//
// Rule one of performance work: MEASURE, don't guess. Intuition about hot spots is wrong most of
// the time. Go ships the whole toolbox in the standard library and toolchain:
//
//	benchmark (testing)   "how fast / how many allocations is this function?"   go test -bench
//	CPU profile (pprof)   "where does the CPU time go?"                         go tool pprof
//	heap profile          "who allocates / holds memory?"                       go tool pprof
//	goroutine/block/mutex "where are goroutines stuck / contending?"            /debug/pprof/...
//	execution trace       "what did the scheduler and GC do over time?"         go tool trace
//
// What this program shows:
//  1. runtime/pprof: wrap a CPU-heavy function in StartCPUProfile/StopCPUProfile (it samples the
//     call stack ~100 times per second), plus a heap profile snapshot written to a temp dir.
//  2. net/http/pprof: the endpoints a production service exposes (-serve mode).
//  3. testing.AllocsPerRun and escape analysis: why some code allocates.
//  4. main_test.go: benchmarks with b.Loop comparing string concatenation vs strings.Builder.
//
// Reading `go tool pprof` output: "flat" = time spent IN that function itself; "cum" (cumulative)
// = time in it plus everything it called. Sort by flat to find the leaf hot spots, by cum to find the
// expensive call trees. Interactive commands: top, list <func> (line-by-line source), web,
// peek. The best view is the browser UI: `go tool pprof -http=:0 cpu.pprof` (flame graph under VIEW).
//
// BENCHMARK COMMANDS (see main_test.go):
//
//	go test -bench=. -benchmem ./08-use-cases/07pprof
//	  -benchmem adds B/op (bytes allocated per iteration) and allocs/op (allocation count).
//	  Fewer allocations usually means less GC work, which is often the real speedup.
//	go test -bench=Concat -benchtime=2s -count=6 ./08-use-cases/07pprof > old.txt   # then after a change > new.txt
//	benchstat old.txt new.txt        # golang.org/x/perf/cmd/benchstat: tells you if a difference is real or noise
//	go test -bench=. -cpuprofile cpu.out -memprofile mem.out ./08-use-cases/07pprof   # profile a benchmark
//
// ESCAPE ANALYSIS: the compiler decides per variable whether it lives on the stack (free, freed
// automatically on return) or the heap (costs an allocation and GC work). A value "escapes" when
// it outlives the function, e.g. you return its address, store it in an interface or send it on a
// channel. See which ones escape with:
//
//	go build -gcflags=-m ./08-use-cases/07pprof        # "escapes to heap" / "does not escape" per variable
//	go build -gcflags='-m -m' ./08-use-cases/07pprof   # also says WHY
//
// Do not contort your code to avoid escapes up front. Use it when a profile shows allocation is the problem.
//
// Try it:
//
//	go run ./08-use-cases/07pprof                        # demo: profile, print commands, exit
//	go run ./08-use-cases/07pprof -top                   # also runs `go tool pprof -top` for you
//	go run ./08-use-cases/07pprof -serve localhost:6060  # live service with pprof endpoints
//	go tool pprof -http=:0 'http://localhost:6060/debug/pprof/profile?seconds=10'   # profile it live
//	curl 'http://localhost:6060/debug/pprof/goroutine?debug=1' | head -20
//
// SECURITY: pprof endpoints leak internals and can be used to hurt your service (a 30s CPU
// profile is real load). Bind them to localhost or an internal admin port, never the public
// listener. Note that `import _ "net/http/pprof"` registers them on http.DefaultServeMux, so if
// you also serve DefaultServeMux publicly you just exposed them. We register them on our own mux.
//
// In industry: continuous profiling (Pyroscope, Grafana Phlare, Google Cloud Profiler, Datadog)
// samples production pprof data all day, so "why did CPU double after Tuesday's deploy" is a
// diff of two flame graphs. Typical Go wins found this way: accidental O(n^2) string building,
// json.Marshal in a hot loop, regexp.MustCompile inside a handler, a mutex hot spot, too many
// small allocations. Profile-guided optimisation (PGO) can even feed a CPU profile back to the
// compiler: put it at ./default.pgo next to main and `go build` uses it for inlining and layout.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	rpprof "runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"
)

// ---- Code under test: four ways to build a big string ----------------------------------

// concat builds the string with +=. Strings are immutable, so every += allocates a NEW string
// and copies everything so far: O(n^2) copying and one allocation per iteration.
func concat(n int) string {
	s := ""
	for i := range n {
		s += strconv.Itoa(i) + ","
	}
	return s
}

// builder uses strings.Builder, which appends into a growable []byte (amortised O(1) per write,
// O(log n) reallocations) and hands back the string without a final copy.
func builder(n int) string {
	var sb strings.Builder
	for i := range n {
		sb.WriteString(strconv.Itoa(i))
		sb.WriteByte(',')
	}
	return sb.String()
}

// builderGrow also pre-sizes the buffer. If you can estimate the final size, Grow removes
// the reallocations entirely. (n*7 is a generous guess for "number," pieces here.)
func builderGrow(n int) string {
	var sb strings.Builder
	sb.Grow(n * 7)
	for i := range n {
		sb.WriteString(strconv.Itoa(i))
		sb.WriteByte(',')
	}
	return sb.String()
}

// builderAppend also avoids the per-number string that strconv.Itoa allocates, by appending the
// digits straight into a reusable stack buffer. Allocation count drops to ~1 per build.
func builderAppend(n int) string {
	var sb strings.Builder
	sb.Grow(n * 7)
	var buf [20]byte // big enough for any int64; does not escape, so it lives on the stack
	for i := range n {
		sb.Write(strconv.AppendInt(buf[:0], int64(i), 10))
		sb.WriteByte(',')
	}
	return sb.String()
}

// ---- CPU-heavy workload to profile -------------------------------------------------------

// fib is a deliberately slow recursive Fibonacci: exponential time, a perfect flame-graph subject.
func fib(n int) int {
	if n < 2 {
		return n
	}
	return fib(n-1) + fib(n-2)
}

// countPrimes uses naive trial division, another classic hot loop.
func countPrimes(limit int) int {
	count := 0
	for n := 2; n < limit; n++ {
		prime := true
		for d := 2; d*d <= n; d++ {
			if n%d == 0 {
				prime = false
				break
			}
		}
		if prime {
			count++
		}
	}
	return count
}

// workload mixes the hot spots, so the profile has a few distinct entries to read.
func workload() (result int) {
	result += fib(38)
	result += countPrimes(1_500_000)
	result += len(concat(20_000)) // allocation-heavy: shows up as runtime.mallocgc / memmove
	return result
}

// ---- Escape analysis examples ------------------------------------------------------------

type point struct{ x, y int }

// sumOnStack: p never leaves the function, so the compiler keeps it on the stack. 0 allocations.
//
//go:noinline
func sumOnStack(a, b int) int {
	p := point{a, b}
	return p.x + p.y
}

// leakToHeap returns a pointer to a local variable. The variable must outlive the call, so it
// "escapes to heap": one allocation per call. (-gcflags=-m prints "moved to heap: p".)
//
//go:noinline
func leakToHeap(a, b int) *point {
	p := point{a, b}
	return &p
}

var sink any // assigning to a package-level interface forces the value to escape

// ---- Demo --------------------------------------------------------------------------------

func main() {
	serve := flag.String("serve", "", "run a live service with pprof endpoints on this address, e.g. localhost:6060")
	top := flag.Bool("top", false, "after profiling, run `go tool pprof -top` on the profile (needs the go tool in PATH)")
	flag.Parse()

	var err error
	if *serve != "" {
		err = runServer(*serve)
	} else {
		err = runDemo(*top)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func runDemo(runTop bool) error {
	dir, err := os.MkdirTemp("", "pprof-demo-*")
	if err != nil {
		return err
	}
	// Not removed on exit on purpose: you want the files to run the printed commands on. The OS
	// temp dir is cleaned up eventually. (Delete it yourself when you are done.)
	cpuPath := filepath.Join(dir, "cpu.pprof")
	heapPath := filepath.Join(dir, "heap.pprof")

	// 1. CPU profile around the workload.
	f, err := os.Create(cpuPath)
	if err != nil {
		return err
	}
	if err := rpprof.StartCPUProfile(f); err != nil { // only one CPU profile can run at a time
		return err
	}
	start := time.Now()
	result := workload()
	rpprof.StopCPUProfile()
	f.Close()
	fmt.Printf("1. CPU profile: workload result=%d, took %v\n   wrote %s\n", result, time.Since(start).Round(time.Millisecond), cpuPath)

	// 2. Heap profile: a snapshot of live allocations as of the most recent GC.
	runtime.GC() // otherwise the snapshot lags by a cycle
	hf, err := os.Create(heapPath)
	if err != nil {
		return err
	}
	if err := rpprof.Lookup("heap").WriteTo(hf, 0); err != nil {
		return err
	}
	hf.Close()
	fmt.Printf("2. Heap profile:\n   wrote %s\n", heapPath)

	fmt.Println("\nLook at them with:")
	fmt.Printf("   go tool pprof -top -nodecount=10 %s     # text table: flat / cum per function\n", cpuPath)
	fmt.Printf("   go tool pprof -http=:0 %s               # browser UI with a flame graph\n", cpuPath)
	fmt.Printf("   go tool pprof -sample_index=alloc_space -top %s   # bytes allocated, by function\n", heapPath)
	fmt.Println("   (inside the interactive prompt: top, list fib, web, quit)")

	if runTop {
		fmt.Println("\n$ go tool pprof -top -nodecount=8", cpuPath)
		if goTool, err := exec.LookPath("go"); err != nil {
			fmt.Println("   (go tool not found in PATH; skipping)")
		} else {
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, goTool, "tool", "pprof", "-top", "-nodecount=8", cpuPath)
			cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
			if err := cmd.Run(); err != nil {
				fmt.Println("   pprof failed:", err)
			}
		}
	}

	// 3. A look at the live /debug/pprof endpoints on a throw-away server.
	if err := demoEndpoints(); err != nil {
		return err
	}

	// 4. Quick benchmark-style comparison (the real one is in main_test.go).
	demoCompare()

	// 5. Escape analysis through the allocation counter.
	fmt.Println("\n5. Escape analysis (testing.AllocsPerRun reports allocations per call):")
	stack := testing.AllocsPerRun(1000, func() { _ = sumOnStack(1, 2) })
	heap := testing.AllocsPerRun(1000, func() { sink = leakToHeap(1, 2) })
	fmt.Printf("   sumOnStack  (value stays local):   %.0f allocs/op\n", stack)
	fmt.Printf("   leakToHeap  (returns &local):      %.0f allocs/op\n", heap)
	fmt.Println("   see why with: go build -gcflags=-m ./08-use-cases/07pprof 2>&1 | grep -E 'moved to heap|escapes'")
	fmt.Println("\nNext: go test -bench=. -benchmem ./08-use-cases/07pprof")
	return nil
}

// pprofMux registers the pprof handlers on OUR mux (not http.DefaultServeMux).
func pprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index) // also serves heap, goroutine, allocs, block, mutex by name
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile) // CPU profile for ?seconds=N
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace) // execution trace for ?seconds=N
	return mux
}

func demoEndpoints() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // loopback only: never expose pprof publicly
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: pprofMux(), ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(ln)
	defer srv.Close()

	resp, err := http.Get("http://" + ln.Addr().String() + "/debug/pprof/goroutine?debug=1")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	lines := strings.Split(string(body), "\n")
	fmt.Printf("\n3. Live endpoint GET /debug/pprof/goroutine?debug=1 -> %d, first lines:\n", resp.StatusCode)
	for _, l := range lines[:min(len(lines), 4)] {
		fmt.Println("   |", l)
	}
	return nil
}

// demoCompare times the four string builders with a plain loop (rough numbers; use `go test -bench`
// for real measurements: it picks iteration counts, warms up and reports allocations).
func demoCompare() {
	const n, reps = 5_000, 20
	fmt.Printf("\n4. Building a string of %d pieces, %d times each (rough timing; benchmarks are more reliable):\n", n, reps)
	for _, c := range []struct {
		name string
		fn   func(int) string
	}{{"concat (+=)", concat}, {"strings.Builder", builder}, {"Builder + Grow", builderGrow}, {"Grow + AppendInt", builderAppend}} {
		start := time.Now()
		for range reps {
			_ = c.fn(n)
		}
		allocs := testing.AllocsPerRun(5, func() { _ = c.fn(n) })
		fmt.Printf("   %-16s %10v per build, %6.0f allocs per build\n", c.name, (time.Since(start) / reps).Round(time.Microsecond), allocs)
	}
}

// runServer is a long-running service with pprof exposed, plus a background loop that creates
// load so there is something to see.
func runServer(addr string) error {
	// Mutex and block profiles are OFF by default because they have overhead. Turn them on
	// deliberately when you suspect lock contention or goroutines blocked on channels/IO.
	runtime.SetMutexProfileFraction(5)
	runtime.SetBlockProfileRate(1000) // sample blocking events lasting >= 1000ns

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() { // synthetic load
		for ctx.Err() == nil {
			workload()
		}
	}()

	mux := pprofMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "pprof demo. Try /debug/pprof/")
	})
	srv := &http.Server{Addr: addr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()
	fmt.Printf("serving on http://%s/debug/pprof/ (Ctrl-C to stop)\n", addr)
	fmt.Printf("  go tool pprof -http=:0 'http://%s/debug/pprof/profile?seconds=10'\n", addr)
	fmt.Printf("  go tool pprof -top http://%s/debug/pprof/heap\n", addr)
	fmt.Printf("  curl 'http://%s/debug/pprof/goroutine?debug=1' | head\n", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}
