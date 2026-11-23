# 02 - Concurrency

Goroutines, channels, `select`, locks, `context`, and the patterns real Go services are built from.
Each lesson is a folder with a heavily commented `main.go` (read the header comment first, then run it).

## Lessons

| # | Folder | Topic |
|---|--------|-------|
| 01 | `01goroutines` | `go` keyword, main exiting early, `sync.WaitGroup` and `wg.Go` (1.25), GOMAXPROCS |
| 02 | `02channels` | unbuffered vs buffered, `close` + `range`, comma-ok, directional types, nil channels |
| 03 | `03select` | `select`, `default`, timeouts with `time.After`, for-select with a done channel |
| 04 | `04sync` | `Mutex`, `RWMutex`, `Once` / `OnceValue`, `sync.Map` |
| 05 | `05atomic` | `atomic.Int64`, `atomic.Bool`, `atomic.Pointer`, atomics vs mutex |
| 06 | `06context` | cancel, timeout, deadline, values, `Cause`, `AfterFunc`, HTTP/DB with ctx |
| 07 | `07workerpool` | bounded worker pool, graceful stop, error collection |
| 08 | `08pipeline` | stages, fan-out / fan-in, cancellation without leaks |
| 09 | `09errgroup` | `errgroup.WithContext`, `SetLimit`, first error cancels the rest |
| 10 | `10ratelimit` | `time.Ticker`, token bucket (`x/time/rate`), semaphore via buffered channel |
| 11 | `11race` | data races and the race detector (`-race`) |
| 12 | `12synctest` | testing time-based code with `testing/synctest` (a package with tests, not a `main`) |
| 13 | `13leaks` | goroutine leaks, double close, missing close, copied mutex |

## Running

Run from the repository root:

```sh
go run ./02-concurrency/01goroutines      # any lesson 01-11 or 13: swap the folder name
go run -race ./02-concurrency/04sync      # with the race detector

# Lesson 11: safe by default; the broken counter only runs with -racy
go run ./02-concurrency/11race
go run -race ./02-concurrency/11race -racy   # prints "WARNING: DATA RACE" and exits with status 66

# Lesson 12 is a package with tests
go test -race -v ./02-concurrency/12synctest

# Everything
go vet ./02-concurrency/...
go test -race ./02-concurrency/...
```

Lessons 09 and 10 use `golang.org/x/sync` and `golang.org/x/time` (already in `go.mod`).

## Mutex vs channel: rule of thumb

- **Channel** when you are passing ownership of data, distributing work, streaming results, or signalling an event (done, cancel, "a value is ready").
- **Mutex** when you are protecting shared state: a cache, a counter, a registry map, a struct's fields.
- **Atomic** for a single independent number or flag.
- If you find yourself building a goroutine that owns a map just so others can send it requests, a `sync.Mutex` around the map is simpler and faster.
- If you find yourself passing a mutex-protected struct between goroutines to coordinate a workflow, you want channels (or `errgroup`).
- Whichever you choose: every goroutine needs an owner and a way to stop, and `go test -race` must be green.

## Interview questions

1. **What is a goroutine and how does it differ from an OS thread?**
   A lightweight, runtime-managed function execution with a small growable stack (starts around 2-8 KB). The Go scheduler multiplexes many goroutines onto few OS threads (M:N), so creating and switching are far cheaper than threads.

2. **What is the difference between buffered and unbuffered channels?**
   Unbuffered: send and receive must rendezvous, so it also synchronises the two goroutines. Buffered: sends block only when the buffer is full and receives only when it is empty, which decouples sender and receiver.

3. **What happens when you send to or receive from a nil channel, or a closed channel?**
   Nil: blocks forever (useful to disable a `select` case). Closed: receive returns buffered values then the zero value with `ok == false`; send panics; closing again panics.

4. **Who should close a channel, and when is closing required?**
   The sender (or one coordinator after all senders are done), never the receiver. Closing is only needed to end a `range` loop or broadcast "no more values"; otherwise the GC reclaims the channel.

5. **What is a data race and how do you find one?**
   Two goroutines access the same memory concurrently, at least one writing, with no synchronisation. Detect with `go test -race` / `go run -race`; fix with a mutex, atomic, or by confining the data to one goroutine.

6. **How does `select` choose among ready cases? What does `default` do?**
   Uniformly at random among ready cases. With no ready case it blocks, unless `default` exists, which runs immediately and makes the operation non-blocking.

7. **How do you cancel a goroutine?**
   Cooperatively: pass a `context.Context` (or a done channel) and have the goroutine check `ctx.Done()` in its loop or pass ctx to blocking calls. Goroutines cannot be killed from outside.

8. **What are the rules for using `context`?**
   First parameter named `ctx`; do not store it in structs; always `defer cancel()`; `WithValue` only for request-scoped data with an unexported key type; cancelling a parent cancels its children.

9. **How would you implement a bounded worker pool, and why bound concurrency?**
   N workers range over a jobs channel and send to a results channel; the producer closes `jobs`, and a goroutine closes `results` after `wg.Wait()`. Bounding protects memory, DB connections and downstream rate limits. `errgroup.SetLimit` is the short version.

10. **What are common causes of goroutine leaks?**
    A send or receive on a channel nobody will service (abandoned after a timeout), a `range` over a channel never closed, a loop with no `ctx.Done()` case, or a missing `wg.Done`. Detect with `runtime.NumGoroutine`, pprof, `goleak`, or `synctest`.

Bonus: *Why can't you copy a `sync.Mutex`?* The copy has independent state, so it no longer protects the original data; `go vet` flags it. Use pointer receivers.
