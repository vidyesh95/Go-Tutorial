// Frameworks 4/4: the notes API with Fiber v3 (github.com/gofiber/fiber/v3).
//
// Fiber is the odd one out. It is NOT built on net/http. It is built on fasthttp, a
// separate HTTP server implementation tuned for low allocations and high throughput, and its
// API is modelled on Express.js. That has real consequences:
//
//   - Handler signature is func(c fiber.Ctx) error. In Fiber v3 Ctx is an interface (in v2 it
//     was *fiber.Ctx). Handlers RETURN an error; return nil on success. Errors returned
//     bubble up through the middleware chain to the app's ErrorHandler, which is where we
//     build the JSON error envelope. (Gin and chi handlers return nothing.)
//   - There is no http.ResponseWriter or *http.Request. Standard-library middleware and
//     libraries (otelhttp, rs/cors, http.TimeoutHandler...) do not plug in directly. Fiber
//     ships its own middleware under github.com/gofiber/fiber/v3/middleware/... (recover,
//     logger, cors, limiter, requestid, ...) and an adaptor package
//     (github.com/gofiber/fiber/v3/middleware/adaptor) that bridges net/http handlers and
//     Fiber handlers in both directions, at some cost in copying.
//   - fasthttp REUSES memory. A Ctx and the strings/[]byte you get from it (c.Params,
//     c.Get, c.Body, c.Query...) are only valid until the handler returns. If you keep one
//     (store it in a map, send it to a goroutine, put it in a global), copy it first
//     (strings.Clone / utils.CopyString), or set fiber.Config{Immutable: true} and pay the
//     allocation cost. Forgetting this causes data that changes under you. Here we parse the
//     id straight into an int and bind the body into a struct, so nothing is retained.
//     (c.Context() is the real context.Context; Ctx itself is pooled and never cancelled.)
//   - c.Bind().Body(&req) picks a decoder from the Content-Type header (JSON, XML, form).
//     Validation is not automatic: either set Config.StructValidator or validate by hand
//     as we do, to keep the code comparable with the other folders.
//   - Routes: app.Get("/notes/:id", h), app.Group("/api/v1"), c.Params("id").
//   - app.Test(req) runs a request through the app in memory (no socket) and returns a
//     *http.Response. We use it for the demo and for main_test.go, instead of httptest.
//   - Shutdown: app.ShutdownWithContext(ctx), as in run() below. (ListenConfig also has a
//     GracefulContext field that does it for you.)
//
// Run:
//
//	go run ./06-frameworks/fiber            # demo requests in memory, then exit
//	go run ./06-frameworks/fiber -serve     # listen on :8080 until Ctrl-C
//
// In industry: Fiber is popular with teams coming from Node/Express and in benchmark-driven
// choices. The trade-off is the separate ecosystem: anything written for net/http needs an
// adaptor, and fasthttp's memory-reuse rules surprise newcomers. Many companies pick
// chi/stdlib/Gin because their tooling (tracing, auth libraries, proxies) just works.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

	"github.com/gofiber/fiber/v3"
	fiberrecover "github.com/gofiber/fiber/v3/middleware/recover" // aliased: "recover" is a Go builtin
)

// ---------- JSON helpers ----------

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError sets the status, writes the envelope, and returns the (nil) error so handlers
// can write `return writeError(...)`.
func writeError(c fiber.Ctx, status int, code, msg string) error {
	return c.Status(status).JSON(errorBody{errorDetail{Code: code, Message: msg}})
}

// ---------- handlers ----------

type API struct {
	store *Store
}

func (a *API) list(c fiber.Ctx) error {
	return c.Status(http.StatusOK).JSON(a.store.List())
}

type createRequest struct {
	Title string `json:"title"`
	Body  string `json:"body"`
}

func (a *API) create(c fiber.Ctx) error {
	var req createRequest
	// Decodes by Content-Type, so the request needs "Content-Type: application/json".
	// Surprise: with NO Content-Type, Bind().Body returns nil and leaves req empty, so the
	// client gets a 422 "title is required" instead of a 400. Use c.Bind().JSON(&req) if you
	// want JSON regardless of the header.
	// The decoded strings are fresh copies, so it is safe to store them (see top comment).
	if err := c.Bind().Body(&req); err != nil {
		return writeError(c, http.StatusBadRequest, "bad_json", "request body must be valid JSON")
	}
	title := strings.TrimSpace(req.Title)
	if msg := validateTitle(title); msg != "" {
		return writeError(c, http.StatusUnprocessableEntity, "validation_failed", msg)
	}
	n := a.store.Create(title, req.Body)
	c.Set("Location", "/api/v1/notes/"+strconv.Itoa(n.ID))
	return c.Status(http.StatusCreated).JSON(n)
}

func parseID(c fiber.Ctx) (int, bool) {
	// c.Params returns a string that is only valid inside this handler; Atoi gives an int,
	// which is a plain value we can keep. Never store the raw string.
	id, err := strconv.Atoi(c.Params("id"))
	return id, err == nil
}

func (a *API) get(c fiber.Ctx) error {
	id, ok := parseID(c)
	if !ok {
		return writeError(c, http.StatusBadRequest, "invalid_id", "id must be an integer")
	}
	n, found := a.store.Get(id)
	if !found {
		return writeError(c, http.StatusNotFound, "not_found", "note not found")
	}
	return c.Status(http.StatusOK).JSON(n)
}

func (a *API) remove(c fiber.Ctx) error {
	id, ok := parseID(c)
	if !ok {
		return writeError(c, http.StatusBadRequest, "invalid_id", "id must be an integer")
	}
	if !a.store.Delete(id) {
		return writeError(c, http.StatusNotFound, "not_found", "note not found")
	}
	return c.SendStatus(http.StatusNoContent)
}

func boom(fiber.Ctx) error {
	panic("something went very wrong")
}

// ---------- error handling and middleware ----------

// newErrorHandler is the ONE place errors returned from handlers/middleware become
// responses. It also receives Fiber's own errors: unknown route (404), wrong method (405).
func newErrorHandler(log *slog.Logger) fiber.ErrorHandler {
	return func(c fiber.Ctx, err error) error {
		if fe, ok := errors.AsType[*fiber.Error](err); ok {
			switch fe.Code {
			case http.StatusNotFound:
				return writeError(c, fe.Code, "not_found", "route not found")
			case http.StatusMethodNotAllowed:
				return writeError(c, fe.Code, "method_not_allowed", "method not allowed")
			}
			return writeError(c, fe.Code, "http_error", fe.Message)
		}
		// Anything else (including a recovered panic) is our bug: log details, hide them from the client.
		log.Error("unhandled error", "err", err, "path", c.Path())
		return writeError(c, http.StatusInternalServerError, "internal", "internal server error")
	}
}

// requestLogger: Fiber runs ErrorHandler only AFTER the whole chain has returned, so when a
// handler returns an error the status is not set yet as the error travels up through us.
// Fiber's built-in logger middleware solves this the same way: call the error handler here,
// then return nil so it doesn't run twice.
func requestLogger(log *slog.Logger, eh fiber.ErrorHandler) fiber.Handler {
	return func(c fiber.Ctx) error {
		start := time.Now()
		if err := c.Next(); err != nil {
			if herr := eh(c, err); herr != nil {
				_ = c.SendStatus(http.StatusInternalServerError)
			}
		}
		// slog formats the values immediately, so passing c.Path() (valid only inside the
		// handler) is fine. Storing it for later would not be.
		log.Info("request", "method", c.Method(), "path", c.Path(),
			"status", c.Response().StatusCode(), "bytes", len(c.Response().Body()),
			"duration", time.Since(start).Round(time.Microsecond))
		return nil
	}
}

// ---------- wiring ----------

func newApp(store *Store, log *slog.Logger) *fiber.App {
	api := &API{store: store}
	eh := newErrorHandler(log)

	app := fiber.New(fiber.Config{
		ErrorHandler: eh,
		BodyLimit:    1 << 20,         // 1 MiB request bodies
		ReadTimeout:  5 * time.Second, // also lets graceful shutdown close idle keep-alive connections
	})

	// Outermost first. recover.New turns a panic into an error returned up the chain, which
	// lands in our ErrorHandler (and so in the JSON envelope), and the logger sees the 500.
	app.Use(requestLogger(log, eh), fiberrecover.New())

	v1 := app.Group("/api/v1")
	v1.Get("/notes", api.list)
	v1.Post("/notes", api.create)
	v1.Get("/notes/:id", api.get)
	v1.Delete("/notes/:id", api.remove)
	v1.Get("/boom", boom)
	return app
}

func main() {
	serve := flag.Bool("serve", false, "listen on :8080 until Ctrl-C instead of running the demo")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && (a.Key == slog.TimeKey || a.Key == "duration") {
				return slog.Attr{}
			}
			return a
		},
	}))
	app := newApp(NewStore(), log)

	if *serve {
		if err := run(app); err != nil {
			log.Error("server failed", "err", err)
			os.Exit(1)
		}
		return
	}
	demo(app)
}

func run(app *fiber.App) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		errCh <- app.Listen(":8080", fiber.ListenConfig{DisableStartupMessage: true})
	}()
	fmt.Println("listening on :8080 (Ctrl-C to stop)")

	select {
	case err := <-errCh: // failed to start, e.g. port in use
		return err
	case <-ctx.Done():
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutCtx); err != nil {
		return err
	}
	if err := <-errCh; err != nil {
		return err
	}
	fmt.Println("shut down cleanly")
	return nil
}

// demo uses app.Test: it pushes a *http.Request through the app in memory (no port) and
// hands back a *http.Response.
func demo(app *fiber.App) {
	call := func(method, path, body string) {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := app.Test(req)
		if err != nil {
			fmt.Println("test request failed:", err)
			return
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		fmt.Printf("=> %-6s %-18s %d %s\n", method, path, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	call("POST", "/api/v1/notes", `{"title":"buy milk","body":"2 litres"}`)
	call("POST", "/api/v1/notes", `{"title":""}`)
	call("POST", "/api/v1/notes", `not json`)
	call("GET", "/api/v1/notes", "")
	call("GET", "/api/v1/notes/1", "")
	call("GET", "/api/v1/notes/99", "")
	call("GET", "/api/v1/notes/abc", "")
	call("DELETE", "/api/v1/notes/1", "")
	call("GET", "/api/v1/notes/1", "")
	call("GET", "/api/v1/boom", "")
	call("GET", "/nope", "")
	call("PUT", "/api/v1/notes", "")
}
