// Lesson 04: sync package (Mutex, RWMutex, Once, Map)
//
// When goroutines share memory, you must synchronise access. Two goroutines
// touching the same variable with at least one writing is a DATA RACE, and
// the result is undefined behaviour (lost updates, torn reads, crashes).
//
// sync.Mutex: Lock() / Unlock(). One goroutine at a time inside the critical section.
//   - Always `defer mu.Unlock()` right after Lock() so early returns and panics unlock.
//   - Keep the mutex next to the data it guards, in the same struct, and document it.
//   - Never copy a Mutex after first use (go vet's copylocks check catches it).
//     Use pointer receivers on types that contain one.
//   - Mutexes are not reentrant: locking twice in one goroutine deadlocks.
//
// sync.RWMutex: many concurrent readers (RLock) OR one writer (Lock). Worth it
// for read-heavy data like caches. For tiny critical sections a plain Mutex
// is often as fast, so measure before reaching for it.
//
// sync.Once: run something exactly once, even under concurrent callers.
// sync.OnceValue (Go 1.21+) wraps a function returning a value: lazy,
// thread-safe initialisation without writing the Once boilerplate.
//
// sync.Map: a concurrent map, but only better than map+Mutex in two cases
// (per its docs): (1) a key is written once and read many times (caches that
// only grow), or (2) goroutines work on disjoint sets of keys. It is untyped
// (any) and awkward. Default to a regular map guarded by a mutex.
//
// In industry: "struct with a mutex and a map" is everywhere (caches, registries,
// in-memory stores). sync.Once/OnceValue guard lazy singletons like loading
// config or compiling a regexp. CI runs `go test -race` to catch missing locks.
package main

import (
	"fmt"
	"sync"
)

// SafeCounter bundles the data and its lock. Callers cannot touch n without
// going through the methods, so they cannot forget to lock.
type SafeCounter struct {
	mu sync.Mutex // guards n
	n  int
}

func (c *SafeCounter) Inc() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.n++
}

func (c *SafeCounter) Value() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

// Cache is read-heavy, so it uses an RWMutex.
type Cache struct {
	mu   sync.RWMutex // guards data
	data map[string]string
}

func NewCache() *Cache { return &Cache{data: make(map[string]string)} }

func (c *Cache) Get(key string) (string, bool) {
	c.mu.RLock() // shared lock: many Gets can run together
	defer c.mu.RUnlock()
	v, ok := c.data[key]
	return v, ok
}

func (c *Cache) Set(key, val string) {
	c.mu.Lock() // exclusive lock: blocks all readers and writers
	defer c.mu.Unlock()
	c.data[key] = val
}

func main() {
	fmt.Println("== 1. Mutex makes the counter safe ==")
	var c SafeCounter
	var wg sync.WaitGroup
	for range 1000 {
		wg.Go(c.Inc)
	}
	wg.Wait()
	// Without the mutex, `n++` (read, add, write) would interleave and we would
	// often see less than 1000. With it, the answer is always exactly 1000.
	fmt.Println("counter:", c.Value())

	fmt.Println("\n== 2. RWMutex cache ==")
	cache := NewCache()
	cache.Set("lang", "Go")
	for range 3 {
		wg.Go(func() {
			v, ok := cache.Get("lang")
			_ = v
			_ = ok
		})
	}
	wg.Wait()
	v, ok := cache.Get("lang")
	fmt.Printf("lang=%q found=%t\n", v, ok)

	fmt.Println("\n== 3. sync.Once: run exactly once ==")
	var once sync.Once
	for range 5 {
		wg.Go(func() {
			once.Do(func() { fmt.Println("init ran (only one goroutine gets here)") })
		})
	}
	wg.Wait()

	fmt.Println("\n== 4. sync.OnceValue: lazy, cached, thread-safe value ==")
	loadConfig := sync.OnceValue(func() map[string]string {
		fmt.Println("loading config (expensive, happens once)")
		return map[string]string{"env": "dev"}
	})
	for range 3 {
		wg.Go(func() { _ = loadConfig() })
	}
	wg.Wait()
	fmt.Println("config:", loadConfig()) // later calls return the cached value instantly

	fmt.Println("\n== 5. sync.Map (use sparingly) ==")
	var m sync.Map
	m.Store("a", 1)
	m.Store("b", 2)
	val, _ := m.Load("a")
	fmt.Println("a =", val.(int)) // values are `any`, so you must type-assert
	actual, loaded := m.LoadOrStore("a", 99)
	fmt.Println("LoadOrStore existing key:", actual, "loaded:", loaded)
	fmt.Println("Prefer map + RWMutex unless you have a write-once/read-many or disjoint-keys workload.")
}
