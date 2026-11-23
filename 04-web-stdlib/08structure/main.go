// Lesson 08: project structure (a preview of the capstone)
//
// Layout of this lesson, a miniature of a production Go service:
//
//	08structure/
//	  main.go                    composition root: builds the object graph, starts the server
//	  internal/domain/           core types + errors; imports nothing from the module
//	  internal/store/            persistence (here: in-memory; later: SQL)
//	  internal/service/          business rules; declares the UserStore interface it needs
//	  internal/handler/          HTTP: routing, JSON, status codes; declares the UserService it needs
//
// Dependency direction (arrows = "imports / calls"):
//
//	main -> handler -> service -> store        and all of them -> domain
//
// Never upward: the store does not know services exist, the service does not know HTTP exists.
// Concrete types are wired together in ONE place, main.go (the "composition root"):
//
//	st  := store.NewMemoryUsers()
//	svc := service.NewUsers(st)      // *MemoryUsers satisfies service.UserStore
//	h   := handler.New(svc, logger)  // *service.Users satisfies handler.UserService
//
// Why it is built this way:
//   - internal/: the compiler forbids other modules from importing these packages, so you may
//     refactor freely; the public surface of a service is its HTTP API, not its Go packages.
//   - Interfaces are small and defined by the CONSUMER ("accept interfaces, return structs").
//     Each layer is unit-tested alone with a fake of the layer below (see the *_test.go files).
//   - Errors flow upward as domain errors (ErrNotFound, ValidationError) wrapped with %w for context,
//     and are translated to HTTP status codes in exactly one function, Handler.fail.
//   - Dependencies are injected via constructors, never globals; main is the only place
//     that calls os.Getenv, builds loggers or opens databases.
//
// Not by accident, but to keep this lesson small: no middleware (add the lesson 04 chain in
// main.go around h.Routes()), no graceful shutdown (lesson 06 block belongs in main.go), no config
// loading (lesson 07). The capstone combines everything.
//
// Run with -serve:
//
//	go run ./04-web-stdlib/08structure -serve
//	curl -i -X POST localhost:8080/users -d '{"email":"ada@example.com","name":"Ada"}'
//	curl -i -X POST localhost:8080/users -d '{"email":"ada@example.com","name":"Again"}'   # 409
//	curl -i -X POST localhost:8080/users -d '{"email":"nope","name":""}'                   # 422
//	curl -i localhost:8080/users
//	curl -i localhost:8080/users/<id>
//
// Run the tests, each layer in isolation: go test -race ./04-web-stdlib/08structure/...
//
// In industry: variations exist (hexagonal / clean architecture, "package by feature" such as
// internal/users/{handler,service,store}.go, a cmd/<binary>/ directory for several binaries,
// sqlc-generated store code, wire or fx for DI at very large scale). All of them share the same
// idea shown here: depend on behaviour via small interfaces, keep HTTP at the edge, keep one
// composition root. Start simple; split packages when a file stops being easy to read.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/handler"
	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/service"
	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/store"
)

// newRouter is the composition root: the only function that knows every concrete type.
func newRouter(logger *slog.Logger) http.Handler {
	st := store.NewMemoryUsers()
	svc := service.NewUsers(st)
	h := handler.New(svc, logger)
	return h.Routes()
}

func main() {
	serve := flag.Bool("serve", false, "run a real server on :8080 until Ctrl-C")
	flag.Parse()

	if *serve {
		logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
		srv := &http.Server{
			Addr:              ":8080",
			Handler:           newRouter(logger),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		}
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
		defer stop()
		go func() {
			<-ctx.Done()
			sc, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			srv.Shutdown(sc)
		}()
		logger.Info("listening", "addr", srv.Addr)
		if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
			log.Fatal(err)
		}
		return
	}
	demo()
}

func demo() {
	ts := httptest.NewServer(newRouter(slog.New(slog.NewTextHandler(os.Stdout, nil))))
	defer ts.Close()

	call := func(label, method, path, body string) {
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rdr)
		resp, err := ts.Client().Do(req)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		fmt.Printf("\n--- %s\n%s %s -> %s\n%s", label, method, path, resp.Status, b)
	}

	call("register", "POST", "/users", `{"email":"Ada@Example.com","name":"Ada Lovelace"}`)
	call("duplicate email (domain error -> 409)", "POST", "/users", `{"email":"ada@example.com","name":"Impostor"}`)
	call("invalid input (validation error -> 422)", "POST", "/users", `{"email":"nope","name":""}`)
	call("unknown id (domain error -> 404)", "GET", "/users/01a207fb-efff-7537-bb76-ef022a6a154b", "")
	call("list", "GET", "/users", "")
}
