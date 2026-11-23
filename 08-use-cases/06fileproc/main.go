// Use case 06: a concurrent file-processing pipeline (streaming CSV, fan-out / fan-in).
//
// The job: a 200,000-row sales CSV arrives (an export, a nightly drop in S3...) and we need
// per-region totals. Two rules of file processing in Go:
//
//  1. STREAM, don't slurp. os.ReadFile / csv.ReadAll load the whole file, plus per-row
//     allocations, into memory: a 5 GB file needs 5+ GB of RAM and gets OOM-killed in a 512 MB
//     container. csv.Reader.Read returns one record at a time, so memory stays flat no matter
//     how big the file is. (This program measures both so you can see the difference.)
//  2. Parallelise the EXPENSIVE part, and batch the hand-off. Reading a file is sequential
//     (one cursor), so one goroutine reads. The per-row work (here: parsing numbers plus a CPU-heavy
//     "fraud score" hash) is independent per row, so N workers do it in parallel.
//     Sending one row per channel operation would spend more time in the channel than in the
//     work; sending BATCHES of 1,000 rows amortises that cost.
//
// Pipeline shape (fan-out then fan-in):
//
//		reader goroutine --batches chan--> worker 1 ... worker N --partial results chan--> merge
//
//	  - Each worker aggregates into its OWN local map, so no mutex is needed on the hot path.
//	  - Each worker sends one partial result when its input is exhausted (fan-in); the caller
//	    merges the maps. "Share by communicating" beats a shared map plus lock here.
//	  - errgroup.WithContext ties the goroutines together: if the reader hits an I/O error, the
//	    shared context is cancelled and every goroutine stops; Wait returns the first error.
//	  - The reader owns and closes the batches channel; workers range over it until it closes.
//
// Money is stored as integer cents. Never sum float64 prices: 0.1 + 0.2 != 0.3.
//
// Does concurrency always win? No. If the per-row work is tiny, the reader (disk + csv parsing)
// is the bottleneck and extra workers just add overhead. Measure first. Try -work 1 and compare.
//
// MEMORY AND CPU PROFILING (more in the next lesson, 07pprof):
//
//	go run ./08-use-cases/06fileproc -memprofile /tmp/mem.out -cpuprofile /tmp/cpu.out
//	go tool pprof -http=:0 /tmp/cpu.out                         # flame graph in the browser
//	go tool pprof -sample_index=alloc_space -top /tmp/mem.out   # who allocates the most?
//	GODEBUG=gctrace=1 go run ./08-use-cases/06fileproc          # one line per garbage collection
//
// In code, runtime.ReadMemStats gives HeapAlloc (live heap now), TotalAlloc (bytes allocated
// over the whole run) and NumGC. The demo samples HeapAlloc to estimate the peak.
//
// Flags: -rows N, -workers N, -work N (hash rounds per row: how CPU-heavy each row is).
//
// In industry: ETL and data-engineering jobs, log processors, report generators and importers
// (billing exports, product feeds, bank statements) all follow this shape. Go suits it because
// the pipeline is a few goroutines and channels, deployment is one binary, and memory use is
// predictable. At larger scale the same pattern moves to Kafka consumers or batch frameworks,
// but the ideas (backpressure via bounded channels, batching, partial aggregation) carry over.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/csv"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"slices"
	"strconv"
	"sync"
	"text/tabwriter"
	"time"

	"golang.org/x/sync/errgroup"
)

var regions = []string{"north-america", "south-america", "europe", "middle-east", "africa", "south-asia", "east-asia", "oceania"}
var products = []string{"widget", "gadget", "gizmo", "doohickey", "thingamajig"}

const columns = 6 // order_id, date, region, product, units, unit_price_cents

// agg is the running total for one region.
type agg struct {
	Orders  int64
	Units   int64
	Cents   int64
	Flagged int64 // orders our (fake) fraud score flagged
}

func (a *agg) add(b agg) {
	a.Orders += b.Orders
	a.Units += b.Units
	a.Cents += b.Cents
	a.Flagged += b.Flagged
}

// Result is what a pipeline run produces.
type Result struct {
	ByRegion map[string]agg
	Rows     int64 // data rows read
	Bad      int64 // rows skipped because they were malformed
}

func (r *Result) merge(o *Result) {
	r.Rows += o.Rows
	r.Bad += o.Bad
	for region, a := range o.ByRegion {
		cur := r.ByRegion[region]
		cur.add(a)
		r.ByRegion[region] = cur
	}
}

func newResult() *Result { return &Result{ByRegion: make(map[string]agg)} }

// processRecord is the per-row work. It is a pure function of its input, which is what makes
// the parallel version safe: no shared state, no locks.
func processRecord(rec []string, rounds int) (region string, a agg, err error) {
	units, err := strconv.Atoi(rec[4])
	if err != nil || units <= 0 {
		return "", agg{}, fmt.Errorf("bad units %q", rec[4])
	}
	price, err := strconv.ParseInt(rec[5], 10, 64)
	if err != nil || price < 0 {
		return "", agg{}, fmt.Errorf("bad price %q", rec[5])
	}
	// Stand-in for expensive per-row work (fraud model, geocoding, signature check...).
	h := sha256.Sum256([]byte(rec[0] + rec[3]))
	for range rounds {
		h = sha256.Sum256(h[:])
	}
	a = agg{Orders: 1, Units: int64(units), Cents: int64(units) * price}
	if h[0] < 8 { // about 3% of orders
		a.Flagged = 1
	}
	return rec[2], a, nil
}

// generateCSV writes a deterministic sales file (seeded PRNG: same data every run) including a
// few malformed rows, which real files always contain.
func generateCSV(path string, rows int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f) // buffered internally; Flush before closing
	rng := rand.New(rand.NewPCG(42, 1042))

	w.Write([]string{"order_id", "date", "region", "product", "units", "unit_price_cents"})
	for i := range rows {
		units := strconv.Itoa(1 + rng.IntN(20))
		switch {
		case i%50_000 == 777:
			units = "N/A" // not a number
		}
		rec := []string{
			strconv.Itoa(1_000_000 + i),
			fmt.Sprintf("2026-%02d-%02d", 1+rng.IntN(12), 1+rng.IntN(28)),
			regions[rng.IntN(len(regions))],
			products[rng.IntN(len(products))],
			units,
			strconv.Itoa(500 + rng.IntN(20_000)),
		}
		if i%60_000 == 123 {
			rec = rec[:4] // truncated row: wrong field count
		}
		if err := w.Write(rec); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// newReader configures csv.Reader for validation. FieldsPerRecord makes short/long rows an
// error (ErrFieldCount) but Read can simply be called again to continue with the next line.
func newReader(r io.Reader) *csv.Reader {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = columns
	// cr.ReuseRecord = true would avoid one allocation per row, but then a record is only
	// valid until the next Read, so you must copy it before handing it to another goroutine.
	return cr
}

// readRecord returns the next data record. skipped=true means "malformed row, already counted".
func readRecord(cr *csv.Reader) (rec []string, skipped bool, err error) {
	rec, err = cr.Read()
	if errors.Is(err, csv.ErrFieldCount) {
		return nil, true, nil
	}
	return rec, false, err
}

// processSequential is the baseline: one goroutine, streaming, one row at a time.
func processSequential(ctx context.Context, path string, rounds int) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	cr := newReader(f)
	cr.Read() // header

	res := newResult()
	for {
		rec, skipped, err := readRecord(cr)
		if errors.Is(err, io.EOF) {
			return res, nil
		}
		if err != nil {
			return nil, err
		}
		res.Rows++
		if skipped {
			res.Bad++
			continue
		}
		region, a, err := processRecord(rec, rounds)
		if err != nil {
			res.Bad++
			continue
		}
		cur := res.ByRegion[region]
		cur.add(a)
		res.ByRegion[region] = cur
	}
}

// processConcurrent is the fan-out / fan-in pipeline.
func processConcurrent(ctx context.Context, path string, workers, batchSize, rounds int) (*Result, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	g, ctx := errgroup.WithContext(ctx)
	// Bounded channel = BACKPRESSURE. If workers are slower than the reader, the reader blocks
	// on send instead of buffering the entire file in memory.
	batches := make(chan [][]string, workers*2)
	partials := make(chan *Result, workers) // one slot per worker, so workers never block on it

	// Stage 1: the single reader. It owns `batches` and closes it when done (or on error).
	var skippedRows int64
	g.Go(func() error {
		defer close(batches)
		cr := newReader(f)
		cr.Read() // header
		batch := make([][]string, 0, batchSize)
		flush := func() error {
			if len(batch) == 0 {
				return nil
			}
			select {
			case batches <- batch:
				batch = make([][]string, 0, batchSize) // a NEW slice: the old one now belongs to a worker
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		for {
			rec, skipped, err := readRecord(cr)
			if errors.Is(err, io.EOF) {
				return flush()
			}
			if err != nil {
				return err
			}
			if skipped {
				skippedRows++ // only this goroutine touches it until Wait returns
				continue
			}
			batch = append(batch, rec)
			if len(batch) == batchSize {
				if err := flush(); err != nil {
					return err
				}
			}
		}
	})

	// Stage 2: N workers. Each aggregates locally and reports once.
	for range workers {
		g.Go(func() error {
			local := newResult()
			for batch := range batches {
				for _, rec := range batch {
					local.Rows++
					region, a, err := processRecord(rec, rounds)
					if err != nil {
						local.Bad++
						continue
					}
					cur := local.ByRegion[region]
					cur.add(a)
					local.ByRegion[region] = cur
				}
				if ctx.Err() != nil {
					return ctx.Err()
				}
			}
			partials <- local
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return nil, err
	}
	close(partials)

	// Stage 3 (fan-in): merge the partial results. Cheap: a handful of regions per worker.
	total := newResult()
	for p := range partials {
		total.merge(p)
	}
	total.Rows += skippedRows
	total.Bad += skippedRows
	return total, nil
}

// measure runs fn while sampling HeapAlloc to estimate peak live heap, and reports allocation.
type measurement struct {
	Elapsed    time.Duration
	PeakHeapMB float64
	AllocMB    float64 // total bytes allocated during the run (churn, not live memory)
	NumGC      uint32
}

func measure(fn func()) measurement {
	runtime.GC()
	var before runtime.MemStats
	runtime.ReadMemStats(&before)

	var peak uint64
	stop := make(chan struct{})
	var sampler sync.WaitGroup
	sampler.Go(func() {
		t := time.NewTicker(5 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				var m runtime.MemStats
				runtime.ReadMemStats(&m)
				peak = max(peak, m.HeapAlloc)
			}
		}
	})
	start := time.Now()
	fn()
	elapsed := time.Since(start)
	close(stop)
	sampler.Wait()

	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	peak = max(peak, after.HeapAlloc)
	const mb = 1 << 20
	return measurement{
		Elapsed:    elapsed,
		PeakHeapMB: float64(peak) / mb,
		AllocMB:    float64(after.TotalAlloc-before.TotalAlloc) / mb,
		NumGC:      after.NumGC - before.NumGC,
	}
}

func money(cents int64) string {
	s := strconv.FormatInt(cents/100, 10)
	for i := len(s) - 3; i > 0; i -= 3 { // thousands separators
		s = s[:i] + "," + s[i:]
	}
	return fmt.Sprintf("$%s.%02d", s, cents%100)
}

func printResult(r *Result) {
	type row struct {
		region string
		a      agg
	}
	var rows []row
	for region, a := range r.ByRegion {
		rows = append(rows, row{region, a})
	}
	slices.SortFunc(rows, func(x, y row) int { return int(y.a.Cents - x.a.Cents) }) // biggest first
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', tabwriter.AlignRight)
	fmt.Fprintln(tw, "REGION\tORDERS\tUNITS\tREVENUE\tFLAGGED\t")
	var all agg
	for _, r := range rows {
		fmt.Fprintf(tw, "%s\t%d\t%d\t%s\t%d\t\n", r.region, r.a.Orders, r.a.Units, money(r.a.Cents), r.a.Flagged)
		all.add(r.a)
	}
	fmt.Fprintf(tw, "TOTAL\t%d\t%d\t%s\t%d\t\n", all.Orders, all.Units, money(all.Cents), all.Flagged)
	tw.Flush()
	fmt.Printf("rows read: %d, malformed rows skipped: %d\n", r.Rows, r.Bad)
}

func sameResult(a, b *Result) bool {
	if a.Rows != b.Rows || a.Bad != b.Bad || len(a.ByRegion) != len(b.ByRegion) {
		return false
	}
	for k, v := range a.ByRegion {
		if b.ByRegion[k] != v {
			return false
		}
	}
	return true
}

func main() {
	rows := flag.Int("rows", 200_000, "number of CSV data rows to generate")
	workers := flag.Int("workers", runtime.GOMAXPROCS(0), "pipeline workers")
	rounds := flag.Int("work", 40, "hash rounds per row (higher = more CPU per row)")
	cpuProf := flag.String("cpuprofile", "", "write a CPU profile of the concurrent run to this file")
	memProf := flag.String("memprofile", "", "write a heap profile (after the concurrent run) to this file")
	flag.Parse()

	if err := run(*rows, *workers, *rounds, *cpuProf, *memProf); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(rows, workers, rounds int, cpuProf, memProf string) error {
	ctx := context.Background()
	dir, err := os.MkdirTemp("", "fileproc-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "sales.csv")

	start := time.Now()
	if err := generateCSV(path, rows); err != nil {
		return err
	}
	info, _ := os.Stat(path)
	fmt.Printf("generated %d rows (%.1f MB) in %v\n", rows, float64(info.Size())/(1<<20), time.Since(start).Round(time.Millisecond))
	fmt.Printf("GOMAXPROCS=%d, workers=%d, hash rounds per row=%d\n\n", runtime.GOMAXPROCS(0), workers, rounds)

	// 1. Sequential baseline (streaming).
	var seq *Result
	mSeq := measure(func() { seq, err = processSequential(ctx, path, rounds) })
	if err != nil {
		return err
	}

	// 2. Concurrent pipeline (optionally profiled).
	if cpuProf != "" {
		f, err := os.Create(cpuProf)
		if err != nil {
			return err
		}
		defer f.Close()
		if err := pprof.StartCPUProfile(f); err != nil {
			return err
		}
	}
	var conc *Result
	mConc := measure(func() { conc, err = processConcurrent(ctx, path, workers, 1000, rounds) })
	if cpuProf != "" {
		pprof.StopCPUProfile()
		fmt.Println("wrote CPU profile:", cpuProf, "-> go tool pprof -http=:0", cpuProf)
	}
	if err != nil {
		return err
	}
	if memProf != "" {
		f, err := os.Create(memProf)
		if err != nil {
			return err
		}
		defer f.Close()
		runtime.GC() // get up-to-date statistics
		if err := pprof.WriteHeapProfile(f); err != nil {
			return err
		}
		fmt.Println("wrote heap profile:", memProf, "-> go tool pprof -sample_index=alloc_space -top", memProf)
	}

	// 3. For contrast: load the whole file into memory first (what NOT to do for big files).
	var all [][]string
	mAll := measure(func() {
		f, e := os.Open(path)
		if e != nil {
			err = e
			return
		}
		defer f.Close()
		cr := csv.NewReader(f)
		cr.FieldsPerRecord = -1 // tolerate the malformed rows; we only care about memory here
		all, err = cr.ReadAll()
	})
	if err != nil {
		return err
	}
	runtime.KeepAlive(all) // ensure the slice stayed live (and counted) until after the measurement

	printResult(conc)
	fmt.Println()
	if !sameResult(seq, conc) {
		return errors.New("BUG: sequential and concurrent results differ")
	}
	fmt.Println("sequential and concurrent results are identical")

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "\nSTRATEGY\tTIME\tPEAK HEAP\tALLOCATED\tGCs")
	for _, r := range []struct {
		name string
		m    measurement
	}{
		{"sequential, streaming", mSeq},
		{fmt.Sprintf("concurrent x%d, streaming", workers), mConc},
		{"csv.ReadAll (whole file in RAM)", mAll},
	} {
		fmt.Fprintf(tw, "%s\t%v\t%.1f MB\t%.1f MB\t%d\n", r.name, r.m.Elapsed.Round(time.Millisecond), r.m.PeakHeapMB, r.m.AllocMB, r.m.NumGC)
	}
	tw.Flush()
	fmt.Printf("\nspeedup: %.2fx with %d workers (ideal is %dx; the single reader and merging limit it)\n",
		float64(mSeq.Elapsed)/float64(mConc.Elapsed), workers, workers)
	fmt.Println("note: peak heap of the streaming runs stays small and flat as -rows grows; ReadAll's grows linearly.")
	return nil
}
