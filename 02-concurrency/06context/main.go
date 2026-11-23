// Lesson 06: context
//
// context.Context carries three things across API boundaries and goroutines:
//  1. cancellation  ("stop, nobody needs the result any more")
//  2. deadlines/timeouts
//  3. request-scoped values (small, read-only metadata)
//
// Rules (from the package docs and Go proverbs):
//   - ctx is the FIRST parameter, named ctx: func Fetch(ctx context.Context, id int) error
//   - Never store a Context in a struct; pass it explicitly down the call chain.
//   - Never pass a nil Context. Use context.Background() at the top of main/tests,
//     or context.TODO() as a placeholder.
//   - Always `defer cancel()` after WithCancel/WithTimeout/WithDeadline, otherwise
//     the context's resources (timer, child links) leak until the parent ends. go vet checks this.
//   - Cancellation is cooperative: your code must watch ctx.Done() (or call
//     APIs that do). Cancelling does not kill a goroutine.
//   - Cancelling a parent cancels all its children, never the other way round.
//   - ctx.Err() is context.Canceled or context.DeadlineExceeded once Done is closed.
//
// Constructors:
//   - WithCancel(parent)           -> ctx, cancel
//   - WithTimeout(parent, d)       -> cancels after duration d
//   - WithDeadline(parent, t)      -> cancels at time t
//   - WithCancelCause / Cause(ctx) -> attach WHY it was cancelled (any error)
//   - WithValue(parent, key, val)  -> attach a value; use an unexported custom key type
//   - AfterFunc(ctx, f)            -> run f in its own goroutine when ctx is done
//
// WithValue is for request-scoped data that crosses APIs: request ID, auth
// user, trace ID. It is NOT for optional function parameters, DB handles,
// loggers-as-config or anything the function needs to work. Those must be
// explicit arguments, because context values are invisible to the compiler.
//
// In industry: every net/http handler gets r.Context(), which is cancelled when
// the client disconnects. You pass it to database/sql (QueryContext), HTTP
// clients (NewRequestWithContext), gRPC, Redis, etc. A middleware sets a request
// ID with WithValue, and the shutdown path cancels a root context on SIGTERM.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"time"
)

// Typed, unexported key type: other packages cannot collide with it, and
// there is no risk of two packages both using the string "requestID".
type ctxKey int

const requestIDKey ctxKey = iota

func WithRequestID(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, requestIDKey, id)
}

func RequestID(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(requestIDKey).(string)
	return id, ok
}

// slowQuery imitates database/sql's QueryContext: it does "work" but stops
// early when ctx is cancelled. This is how all ctx-aware code is shaped.
func slowQuery(ctx context.Context, d time.Duration) (string, error) {
	select {
	case <-time.After(d):
		return "rows", nil
	case <-ctx.Done():
		return "", ctx.Err() // context.Canceled or DeadlineExceeded
	}
}

func main() {
	fmt.Println("== 1. WithCancel ==")
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel() // e.g. the user clicked "stop"
	}()
	_, err := slowQuery(ctx, time.Second)
	fmt.Println("err:", err, "| is Canceled:", errors.Is(err, context.Canceled))
	cancel() // calling cancel again is harmless; defer it in real code

	fmt.Println("\n== 2. WithTimeout ==")
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	_, err = slowQuery(ctx, time.Second)
	fmt.Println("err:", err, "| is DeadlineExceeded:", errors.Is(err, context.DeadlineExceeded))

	fmt.Println("\n== 3. WithDeadline (an absolute time) ==")
	deadline := time.Now().Add(30 * time.Millisecond)
	ctx, cancel = context.WithDeadline(context.Background(), deadline)
	defer cancel()
	d, _ := ctx.Deadline()
	fmt.Println("has deadline in the future:", time.Until(d) > 0)
	_, err = slowQuery(ctx, time.Second)
	fmt.Println("err:", err)

	fmt.Println("\n== 4. WithValue: request-scoped data with a typed key ==")
	ctx = WithRequestID(context.Background(), "req-42")
	if id, ok := RequestID(ctx); ok {
		fmt.Println("request id:", id)
	}
	_, ok := RequestID(context.Background())
	fmt.Println("missing id found:", ok)

	fmt.Println("\n== 5. WithCancelCause + context.Cause ==")
	ctx, cancelCause := context.WithCancelCause(context.Background())
	cancelCause(errors.New("upstream payment service is down"))
	fmt.Println("ctx.Err():       ", ctx.Err())          // always plain Canceled
	fmt.Println("context.Cause(): ", context.Cause(ctx)) // the real reason

	fmt.Println("\n== 6. AfterFunc: cleanup when ctx ends ==")
	ctx, cancel = context.WithCancel(context.Background())
	cleaned := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		fmt.Println("AfterFunc: releasing resources")
		close(cleaned)
	})
	_ = stop // stop() would unregister f if the resource was released another way
	cancel()
	<-cleaned

	fmt.Println("\n== 7. HTTP client honouring ctx ==")
	// A local in-process test server whose handler is slow. No real network.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(500 * time.Millisecond):
			fmt.Fprint(w, "slow response")
		case <-r.Context().Done(): // server side also sees the client hang up
		}
	}))
	defer srv.Close()

	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL, nil)
	if err != nil {
		fmt.Println("build request:", err)
		return
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Println("request aborted:", errors.Is(err, context.DeadlineExceeded))
	} else {
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		fmt.Println("body:", string(b))
	}

	fmt.Println("\n== 8. Database call honouring ctx (simulated) ==")
	ctx, cancel = context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	// With database/sql this is: db.QueryRowContext(ctx, "SELECT ...")
	if _, err := slowQuery(ctx, time.Second); err != nil {
		fmt.Println("query failed fast:", err)
	}
}
