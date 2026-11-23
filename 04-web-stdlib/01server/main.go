// Lesson 01: the net/http server
//
// Everything in Go's HTTP server hangs off one tiny interface:
//
//		type Handler interface { ServeHTTP(http.ResponseWriter, *http.Request) }
//
//	  - http.Handler     : anything with a ServeHTTP method (a struct, usually).
//	  - http.HandlerFunc : an adapter type, so a plain func(w, r) becomes a Handler.
//	    (It is just `type HandlerFunc func(ResponseWriter, *Request)` with a
//	    ServeHTTP method that calls itself. Methods on func types are legal Go.)
//	  - http.ServeMux    : a Handler that routes by URL pattern to other Handlers.
//	  - http.Server      : owns the listener loop, timeouts and shutdown.
//
// ResponseWriter rules (the #1 source of beginner bugs):
//  1. Set headers FIRST:   w.Header().Set("Content-Type", ...)
//  2. Then w.WriteHeader(status)  (optional; default is 200)
//  3. Then write the body: w.Write(...)
//     The first call to Write (or WriteHeader) flushes the status line and headers.
//     Headers set after that are silently ignored, and a second WriteHeader logs
//     "superfluous response.WriteHeader call". After a handler returns you must
//     not touch w or r again.
//
// Request fields you will use daily: r.Method, r.URL.Path, r.URL.Query(),
// r.Header.Get, r.Body (an io.ReadCloser, server closes it for you), r.Host,
// r.RemoteAddr, r.Context() (cancelled when the client goes away).
//
// Why http.ListenAndServe(":8080", mux) is unsafe in production:
// it uses a zero-value http.Server, which has NO timeouts. A client that opens a
// connection and sends one byte per minute (a "slowloris" attack), or one that
// never reads the response, holds a goroutine and a file descriptor forever.
// Always build your own http.Server:
//   - ReadHeaderTimeout: max time to read request headers (set this at minimum)
//   - ReadTimeout:       max time to read the entire request incl. body
//   - WriteTimeout:      max time from end of header read to end of response write
//   - IdleTimeout:       how long keep-alive connections may sit unused
//
// Default mode of this program starts a server on a random local port, calls it a
// few times with an http.Client, prints the results and shuts down.
// With -serve it listens on :8080 until Ctrl-C:
//
//	go run ./04-web-stdlib/01server -serve
//	curl -i localhost:8080/hello
//	curl -i 'localhost:8080/echo?name=gopher' -H 'X-Demo: 1' -d 'body text'
//	curl -i localhost:8080/teapot
//
// In industry: nobody serves through DefaultServeMux or bare ListenAndServe. Teams
// build one *http.Server per binary with explicit timeouts, hand it a mux they own
// (so no package can sneak routes in via init()), and wrap it in graceful shutdown
// (lesson 06). Frameworks (chi, gin, echo) are thin layers over exactly these types.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"time"
)

// greeter is a Handler implemented by a struct. Use this form when the handler
// needs dependencies (a logger, a store, config) - they become struct fields.
type greeter struct {
	greeting string
}

func (g greeter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		name = "world"
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "%s, %s!\n", g.greeting, name)
}

// echo is a plain function; HandlerFunc adapts it when registered with mux.HandleFunc.
// It shows the most useful Request fields.
func echo(w http.ResponseWriter, r *http.Request) {
	// Always bound the body you read. (Lesson 03 uses http.MaxBytesReader.)
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "cannot read body", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	fmt.Fprintf(w, "method : %s\n", r.Method)
	fmt.Fprintf(w, "path   : %s\n", r.URL.Path)
	fmt.Fprintf(w, "query  : %s\n", r.URL.RawQuery)
	fmt.Fprintf(w, "proto  : %s\n", r.Proto)
	fmt.Fprintf(w, "x-demo : %q\n", r.Header.Get("X-Demo")) // canonicalised, case-insensitive
	fmt.Fprintf(w, "body   : %q\n", body)
}

// teapot shows the header -> status -> body order.
func teapot(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8") // 1. headers
	w.Header().Set("X-Brew", "refused")
	w.WriteHeader(http.StatusTeapot)     // 2. status
	io.WriteString(w, "I am a teapot\n") // 3. body
	// BUG if you did this here: w.Header().Set("X-Too-Late", "1")
	// the headers are already on the wire, so it would have no effect.
}

// newMux builds the router. Returning a fresh mux (instead of using the global
// http.DefaultServeMux) keeps tests isolated and avoids hidden global state.
func newMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/hello", greeter{greeting: "Hello"}) // Handler value
	mux.HandleFunc("/echo", echo)                    // func, adapted to HandlerFunc
	mux.HandleFunc("/teapot", teapot)
	// An explicit HandlerFunc conversion does the same as HandleFunc:
	mux.Handle("/ping", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, "pong\n")
	}))
	return mux
}

// newServer returns a production-minded http.Server. Pick numbers from your real
// traffic; these are reasonable starting points for a JSON API.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: 5 * time.Second,  // defends against slowloris
		ReadTimeout:       10 * time.Second, // whole request incl. body
		WriteTimeout:      15 * time.Second, // handler + response; > your slowest handler
		IdleTimeout:       60 * time.Second, // keep-alive connections
		MaxHeaderBytes:    1 << 20,          // default is also 1 MiB; shown for clarity
	}
}

func main() {
	serve := flag.Bool("serve", false, "run a real server on :8080 until Ctrl-C")
	flag.Parse()

	srv := newServer(":8080", newMux())
	if *serve {
		runForever(srv)
		return
	}
	demo(srv)
}

// runForever blocks until Ctrl-C. Lesson 06 explains the shutdown dance in depth.
func runForever(srv *http.Server) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	go func() {
		<-ctx.Done()
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutdownCtx)
	}()

	log.Printf("listening on %s (Ctrl-C to stop)", srv.Addr)
	// ListenAndServe always returns a non-nil error; ErrServerClosed means "we asked it to stop".
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}

// demo serves on a random free port, makes a few requests, then shuts down.
func demo(srv *http.Server) {
	// Port 0 = "kernel, pick any free port". Binding ourselves (instead of
	// srv.ListenAndServe) lets us learn the address before serving.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	go srv.Serve(ln) // Serve blocks, so run it in a goroutine
	base := "http://" + ln.Addr().String()
	fmt.Println("server listening on", base)

	client := &http.Client{Timeout: 2 * time.Second}

	show(client, "GET /hello", base+"/hello?name=gopher", "", nil)
	show(client, "POST /echo", base+"/echo?x=1", "hi there", map[string]string{"X-Demo": "42"})
	show(client, "GET /teapot", base+"/teapot", "", nil)
	show(client, "GET /missing (404 from mux)", base+"/missing", "", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatal(err)
	}
	fmt.Println("server shut down cleanly")
}

func show(c *http.Client, label, url, body string, hdr map[string]string) {
	method := http.MethodGet
	var rdr io.Reader
	if body != "" {
		method = http.MethodPost
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, rdr)
	if err != nil {
		log.Fatal(err)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	fmt.Printf("\n--- %s\nstatus: %s\ncontent-type: %s\n%s", label, resp.Status, resp.Header.Get("Content-Type"), b)
}
