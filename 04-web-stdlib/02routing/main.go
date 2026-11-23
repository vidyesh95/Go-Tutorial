// Lesson 02: routing with the Go 1.22+ ServeMux
//
// Since Go 1.22 the standard mux understands methods and wildcards, which removed
// the main reason people reached for third-party routers.
//
// Pattern syntax:  [METHOD ][HOST]/[PATH]
//
//	"GET /tasks/{id}"       method + single-segment wildcard; read it with r.PathValue("id")
//	"POST /tasks"           only POST (GET also implies HEAD)
//	"GET /files/{path...}"  trailing wildcard swallows the rest of the path ("a/b/c.txt")
//	"GET /{$}"              exact match for "/" only. Plain "/" is a catch-all subtree!
//	"/static/"              trailing slash = subtree: matches /static/ and everything below it
//	"api.example.com/ping"  host-specific pattern (matches the Host header)
//
// Precedence: the MOST SPECIFIC pattern wins, regardless of registration order.
//   - "/tasks/latest" beats "/tasks/{id}" (literal beats wildcard)
//   - "GET /tasks/{id}" beats "/tasks/{id}" (method beats no method)
//   - host patterns beat host-less ones
//
// If two patterns overlap and neither is more specific (e.g. "/a/{x}/b" and
// "/a/b/{y}" both match /a/b/b), registering the second one PANICS at startup.
// That is good: routing ambiguity is caught the moment the server boots.
//
// Automatic responses:
//   - path matches, method does not  -> 405 Method Not Allowed + an Allow header
//   - nothing matches                -> 404 Not Found
//   - "/dir" when only "/dir/" is registered -> 301 redirect to "/dir/"
//
// Query parameters are not part of the pattern. Use r.URL.Query() which returns
// url.Values (map[string][]string): .Get("k") for the first value, ["k"] for all.
// Always validate and convert them yourself (strconv.Atoi etc.); they are strings.
//
// Route groups: build a sub-mux and mount it under a prefix with http.StripPrefix.
// StripPrefix removes the prefix from r.URL.Path before the sub-mux sees it, so the
// sub-mux does not need to know where it is mounted (/api/v1 today, /api/v2 tomorrow).
// One gotcha: handlers that build absolute URLs (Location headers) must re-add the prefix.
//
// Run with -serve to poke it yourself:
//
//	go run ./04-web-stdlib/02routing -serve
//	curl -i localhost:8080/
//	curl -i localhost:8080/tasks/42
//	curl -i localhost:8080/tasks/latest
//	curl -i -X DELETE localhost:8080/tasks/42          # 405 + Allow header
//	curl -i localhost:8080/files/docs/guide/intro.md
//	curl -i 'localhost:8080/search?q=go&limit=5&tag=web&tag=api'
//	curl -i localhost:8080/api/v1/users/7
//	curl -i -H 'Host: api.example.com' localhost:8080/ping
//
// In industry: a ServeMux is enough for most services. Teams that adopt chi/gorilla
// mostly want middleware grouping (r.Group, r.Use); with plain net/http you get the
// same by wrapping sub-muxes (lesson 04). Whatever the router, keep it behind the
// http.Handler interface so you can swap it without touching handlers.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"
)

func newHandler() http.Handler {
	mux := http.NewServeMux()

	// Exact root only. Without {$} this pattern would also catch every unknown path.
	mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "home")
	})

	// Method + wildcard. PathValue returns "" if the name is not in the pattern.
	mux.HandleFunc("GET /tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil {
			http.Error(w, "id must be a number", http.StatusBadRequest)
			return
		}
		fmt.Fprintf(w, "task %d\n", id)
	})

	// More specific than /tasks/{id}, so it wins for exactly /tasks/latest.
	mux.HandleFunc("GET /tasks/latest", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "the latest task")
	})

	mux.HandleFunc("POST /tasks", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		fmt.Fprintln(w, "created")
	})

	// "{path...}" must be the last segment.
	mux.HandleFunc("GET /files/{path...}", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "file path = %q\n", r.PathValue("path"))
	})

	// Query parameters.
	mux.HandleFunc("GET /search", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		limit := 10 // default
		if s := q.Get("limit"); s != "" {
			n, err := strconv.Atoi(s)
			if err != nil || n < 1 || n > 100 {
				http.Error(w, "limit must be 1..100", http.StatusBadRequest)
				return
			}
			limit = n
		}
		fmt.Fprintf(w, "q=%q limit=%d tags=%v\n", q.Get("q"), limit, q["tag"])
	})

	// Host-specific pattern.
	mux.HandleFunc("api.example.com/ping", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "pong from api.example.com")
	})

	// Route group: everything under /api/v1 is a separate mux.
	mux.Handle("/api/v1/", http.StripPrefix("/api/v1", usersAPI()))

	return mux
}

// usersAPI knows nothing about /api/v1; its routes start at "/".
func usersAPI() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		// After StripPrefix, r.URL.Path is "/users/7", not "/api/v1/users/7".
		fmt.Fprintf(w, "user %s (path seen by sub-mux: %s)\n", r.PathValue("id"), r.URL.Path)
	})
	return mux
}

func main() {
	serve := flag.Bool("serve", false, "run a real server on :8080 until Ctrl-C")
	flag.Parse()

	if *serve {
		runForever(&http.Server{Addr: ":8080", Handler: newHandler(), ReadHeaderTimeout: 5 * time.Second})
		return
	}
	demo()
}

func demo() {
	// httptest.NewServer picks a random port; handy for demos as well as tests.
	ts := httptest.NewServer(newHandler())
	defer ts.Close()
	fmt.Println("server on", ts.URL)

	calls := []struct{ method, path, host string }{
		{"GET", "/", ""},
		{"GET", "/anything-else", ""}, // "/" alone would have matched this; {$} prevents it
		{"GET", "/tasks/42", ""},
		{"GET", "/tasks/latest", ""},
		{"GET", "/tasks/abc", ""},
		{"POST", "/tasks", ""},
		{"DELETE", "/tasks/42", ""}, // path matches, method does not: 405 + Allow
		{"GET", "/files/docs/guide/intro.md", ""},
		{"GET", "/search?q=go&limit=5&tag=web&tag=api", ""},
		{"GET", "/search?limit=1000", ""},
		{"GET", "/api/v1/users/7", ""},
		{"GET", "/ping", ""}, // no host pattern match -> 404
		{"GET", "/ping", "api.example.com"},
	}
	for _, c := range calls {
		do(ts, c.method, c.path, c.host)
	}

	fmt.Println("\n--- conflicting patterns panic at registration time")
	conflict()
}

func do(ts *httptest.Server, method, path, host string) {
	req, err := http.NewRequest(method, ts.URL+path, nil)
	if err != nil {
		log.Fatal(err)
	}
	if host != "" {
		req.Host = host // overrides the Host header the client sends
		path = host + path
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		log.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	allow := ""
	if a := resp.Header.Get("Allow"); a != "" {
		allow = "  [Allow: " + a + "]"
	}
	fmt.Printf("%-6s %-42s -> %d %s%s\n", method, path, resp.StatusCode, strings.TrimSpace(string(body)), allow)
}

func conflict() {
	defer func() {
		// The panic text also embeds source file paths; keep just the pattern.
		msg, _, _ := strings.Cut(fmt.Sprint(recover()), " (registered")
		fmt.Println("recovered panic:", msg, "conflicts with an already registered pattern")
	}()
	mux := http.NewServeMux()
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) {})
	mux.Handle("/a/{x}/b", h)
	mux.Handle("/a/b/{y}", h) // both match /a/b/b and neither is more specific
}

func runForever(srv *http.Server) {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	go func() {
		<-ctx.Done()
		sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(sc)
	}()
	log.Printf("listening on %s (Ctrl-C to stop)", srv.Addr)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
