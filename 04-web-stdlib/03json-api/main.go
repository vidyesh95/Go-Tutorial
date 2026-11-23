// Lesson 03: a JSON CRUD API (Tasks) with the standard library only
//
// Endpoints (in-memory store, so data vanishes on restart):
//
//	POST   /tasks        create        -> 201 Created + Location header
//	GET    /tasks        list          -> 200   (optional ?done=true|false)
//	GET    /tasks/{id}   fetch one     -> 200 / 404
//	PATCH  /tasks/{id}   partial edit  -> 200 / 404 / 409 / 422
//	DELETE /tasks/{id}   remove        -> 204 No Content / 404
//
// What this lesson teaches:
//   - Three helpers do all the I/O so handlers stay small:
//     writeJSON, readJSON (decode + limits + strictness) and writeError.
//   - Request bodies are untrusted. http.MaxBytesReader caps the size (a hostile
//     client otherwise streams gigabytes into your memory), and
//     Decoder.DisallowUnknownFields turns client typos ("titel") into errors
//     instead of silently ignoring them. We also reject trailing garbage after the
//     first JSON value.
//   - One error envelope for EVERY failure, so clients write one error parser:
//     {"error":{"code":"validation_failed","message":"...","fields":{"title":"required"}}}
//     code is a stable machine-readable string; message is for humans.
//   - Status codes carry meaning (see 04-web-stdlib/README.md for the cheat-sheet):
//     400 malformed JSON / unknown field, 413 body too large, 415 wrong Content-Type,
//     422 well-formed JSON that fails validation, 404 unknown id, 409 conflict with
//     current state (duplicate title), 201 + Location on create, 204 for empty success.
//   - Concurrency: every request runs in its own goroutine, so a shared map needs a
//     lock. sync.RWMutex lets many readers in at once; writers are exclusive. Run the
//     tests with -race to prove it.
//   - IDs are UUIDv7 from the stdlib `uuid` package (new in Go 1.27). v7 embeds a
//     millisecond timestamp in the high bits, so ids sort by creation time, which is
//     kind to database indexes (unlike random v4) and still unguessable enough for
//     public ids. We sort the list by id to get creation order for free.
//   - PATCH uses pointer fields in the request struct so "field absent" (nil) can be
//     told apart from "set to zero value" (e.g. done:false).
//
// Run with -serve and try it with curl:
//
//	go run ./04-web-stdlib/03json-api -serve
//	curl -i -X POST localhost:8080/tasks -d '{"title":"learn Go","priority":2}'
//	curl -i localhost:8080/tasks
//	curl -i -X PATCH localhost:8080/tasks/<id> -d '{"done":true}'
//	curl -i -X POST localhost:8080/tasks -d '{"title":""}'          # 422 with field errors
//	curl -i -X POST localhost:8080/tasks -d '{"titel":"typo"}'      # 400 unknown field
//	curl -i -X DELETE localhost:8080/tasks/<id>                     # 204
//
// In industry: the same shape appears everywhere, except the map becomes a database
// behind an interface (lessons 03-sql-sqlite and 08structure), and the error envelope
// follows the team's API guidelines (many adopt RFC 9457 "problem details").
// Request/response structs are kept separate from domain/DB structs so the API can
// evolve without breaking storage, and vice versa.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"maps"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
	"uuid"
)

// ---------- domain ----------

type Task struct {
	ID        uuid.UUID `json:"id"` // uuid.UUID implements MarshalText, so JSON gets the canonical string
	Title     string    `json:"title"`
	Done      bool      `json:"done"`
	Priority  int       `json:"priority"` // 1 (highest) .. 5
	CreatedAt time.Time `json:"created_at"`
}

// Request types are separate from Task: clients may not set id or created_at.
type createTaskRequest struct {
	Title    string `json:"title"`
	Priority *int   `json:"priority"` // nil -> default
}

type updateTaskRequest struct {
	Title    *string `json:"title"`
	Done     *bool   `json:"done"`
	Priority *int    `json:"priority"`
}

const (
	maxBodyBytes    = 1 << 20 // 1 MiB; JSON APIs rarely need more
	defaultPriority = 3
)

// validateTitle and validatePriority return "" when OK, otherwise a message for the client.
func validateTitle(s string) string {
	switch n := utf8.RuneCountInString(strings.TrimSpace(s)); {
	case n == 0:
		return "is required"
	case n > 100:
		return "must be at most 100 characters"
	}
	return ""
}

func validatePriority(p int) string {
	if p < 1 || p > 5 {
		return "must be between 1 and 5"
	}
	return ""
}

// ---------- store ----------

var (
	errNotFound  = errors.New("task not found")
	errDuplicate = errors.New("a task with this title already exists")
)

// taskStore is safe for concurrent use. The mutex guards the map; never copy a
// struct containing a mutex (go vet's copylocks check catches it).
type taskStore struct {
	mu    sync.RWMutex
	tasks map[uuid.UUID]Task
}

func newTaskStore() *taskStore {
	return &taskStore{tasks: make(map[uuid.UUID]Task)}
}

func (s *taskStore) titleTakenLocked(title string, except uuid.UUID) bool {
	for id, t := range s.tasks {
		if id != except && strings.EqualFold(t.Title, title) {
			return true
		}
	}
	return false
}

func (s *taskStore) Create(title string, priority int) (Task, error) {
	s.mu.Lock() // write lock: exclusive
	defer s.mu.Unlock()
	if s.titleTakenLocked(title, uuid.Nil()) {
		return Task{}, errDuplicate
	}
	t := Task{ID: uuid.NewV7(), Title: title, Priority: priority, CreatedAt: time.Now().UTC()}
	s.tasks[t.ID] = t
	return t, nil
}

func (s *taskStore) Get(id uuid.UUID) (Task, error) {
	s.mu.RLock() // read lock: shared
	defer s.mu.RUnlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, errNotFound
	}
	return t, nil
}

// List returns tasks in creation order. Maps iterate in random order, and returning
// a stable order is part of a good API contract.
func (s *taskStore) List(done *bool) []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.tasks))
	for t := range maps.Values(s.tasks) {
		if done == nil || t.Done == *done {
			out = append(out, t)
		}
	}
	slices.SortFunc(out, func(a, b Task) int { return a.ID.Compare(b.ID) }) // v7 => time order
	return out
}

func (s *taskStore) Update(id uuid.UUID, req updateTaskRequest) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.tasks[id]
	if !ok {
		return Task{}, errNotFound
	}
	if req.Title != nil {
		if s.titleTakenLocked(*req.Title, id) {
			return Task{}, errDuplicate
		}
		t.Title = *req.Title
	}
	if req.Done != nil {
		t.Done = *req.Done
	}
	if req.Priority != nil {
		t.Priority = *req.Priority
	}
	s.tasks[id] = t // t is a copy (map values are not addressable), so write it back
	return t, nil
}

func (s *taskStore) Delete(id uuid.UUID) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.tasks[id]; !ok {
		return errNotFound
	}
	delete(s.tasks, id)
	return nil
}

// ---------- JSON helpers ----------

type errorBody struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

type errorEnvelope struct {
	Error errorBody `json:"error"`
}

// writeJSON marshals first and writes second. If marshalling fails we can still
// send a clean 500; streaming straight into w would leave a half-written body.
func writeJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		log.Printf("writeJSON: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		io.WriteString(w, `{"error":{"code":"internal","message":"internal server error"}}`)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}

func writeError(w http.ResponseWriter, status int, code, message string, fields map[string]string) {
	writeJSON(w, status, errorEnvelope{Error: errorBody{Code: code, Message: message, Fields: fields}})
}

// readJSON decodes the request body into dst. On failure it has ALREADY written the
// error response and returns false, so callers just `return`.
func readJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	// Content-Type check: 415 tells clients they sent the wrong format. We accept a
	// missing header too, because curl -d sends form-encoded by default and being
	// strict here would only confuse beginners; many production APIs are strict.
	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || (mt != "application/json" && mt != "application/x-www-form-urlencoded") {
			writeError(w, http.StatusUnsupportedMediaType, "unsupported_media_type", "Content-Type must be application/json", nil)
			return false
		}
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes) // also tells the server to close the connection when exceeded
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()

	err := dec.Decode(dst)
	if err == nil {
		// A second Decode must hit EOF; otherwise the body was `{...} garbage`.
		if err2 := dec.Decode(&struct{}{}); err2 != io.EOF {
			err = errors.New("body must contain a single JSON object")
		}
	}
	if err == nil {
		return true
	}

	if mbe, ok := errors.AsType[*http.MaxBytesError](err); ok {
		writeError(w, http.StatusRequestEntityTooLarge, "body_too_large",
			fmt.Sprintf("body must be at most %d bytes", mbe.Limit), nil)
		return false
	}
	msg := "invalid request body: " + err.Error()
	if errors.Is(err, io.EOF) {
		msg = "request body is empty"
	}
	// err text for DisallowUnknownFields is `json: unknown field "titel"`: safe and useful to echo.
	writeError(w, http.StatusBadRequest, "bad_request", msg, nil)
	return false
}

// ---------- handlers ----------

// api holds the handlers' dependencies; handlers are methods so they can reach them.
type api struct {
	store *taskStore
}

func (a *api) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /tasks", a.createTask)
	mux.HandleFunc("GET /tasks", a.listTasks)
	mux.HandleFunc("GET /tasks/{id}", a.getTask)
	mux.HandleFunc("PATCH /tasks/{id}", a.updateTask)
	mux.HandleFunc("DELETE /tasks/{id}", a.deleteTask)
	return mux
}

// pathID parses {id}. A malformed id can never exist, so it is simply a 404.
func pathID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "not_found", "task not found", nil)
		return uuid.UUID{}, false
	}
	return id, true
}

// storeError maps store errors to HTTP in ONE place.
func storeError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		writeError(w, http.StatusNotFound, "not_found", err.Error(), nil)
	case errors.Is(err, errDuplicate):
		writeError(w, http.StatusConflict, "conflict", err.Error(), nil)
	default:
		log.Printf("store error: %v", err) // log details, never leak them to the client
		writeError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
	}
}

func (a *api) createTask(w http.ResponseWriter, r *http.Request) {
	var req createTaskRequest
	if !readJSON(w, r, &req) {
		return
	}

	fields := map[string]string{}
	if msg := validateTitle(req.Title); msg != "" {
		fields["title"] = msg
	}
	priority := defaultPriority
	if req.Priority != nil {
		priority = *req.Priority
		if msg := validatePriority(priority); msg != "" {
			fields["priority"] = msg
		}
	}
	if len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "one or more fields are invalid", fields)
		return
	}

	t, err := a.store.Create(strings.TrimSpace(req.Title), priority)
	if err != nil {
		storeError(w, err)
		return
	}
	w.Header().Set("Location", "/tasks/"+t.ID.String()) // where the new resource lives
	writeJSON(w, http.StatusCreated, t)
}

func (a *api) listTasks(w http.ResponseWriter, r *http.Request) {
	var done *bool
	switch r.URL.Query().Get("done") {
	case "":
	case "true":
		done = new(true) // Go 1.26: new(expr) returns a pointer to the value
	case "false":
		done = new(false)
	default:
		writeError(w, http.StatusBadRequest, "bad_request", "done must be true or false", nil)
		return
	}
	tasks := a.store.List(done)
	// Wrap lists in an object so you can add "next_cursor", "total", ... later
	// without breaking clients.
	writeJSON(w, http.StatusOK, map[string]any{"tasks": tasks, "count": len(tasks)})
}

func (a *api) getTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	t, err := a.store.Get(id)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *api) updateTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	var req updateTaskRequest
	if !readJSON(w, r, &req) {
		return
	}
	fields := map[string]string{}
	if req.Title != nil {
		if msg := validateTitle(*req.Title); msg != "" {
			fields["title"] = msg
		}
	}
	if req.Priority != nil {
		if msg := validatePriority(*req.Priority); msg != "" {
			fields["priority"] = msg
		}
	}
	if len(fields) > 0 {
		writeError(w, http.StatusUnprocessableEntity, "validation_failed", "one or more fields are invalid", fields)
		return
	}
	t, err := a.store.Update(id, req)
	if err != nil {
		storeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (a *api) deleteTask(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(w, r)
	if !ok {
		return
	}
	if err := a.store.Delete(id); err != nil {
		storeError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent) // success, nothing to say: no body
}

// ---------- main / demo ----------

func main() {
	serve := flag.Bool("serve", false, "run a real server on :8080 until Ctrl-C")
	flag.Parse()

	a := &api{store: newTaskStore()}
	if *serve {
		runForever(&http.Server{
			Addr:              ":8080",
			Handler:           a.routes(),
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       10 * time.Second,
			WriteTimeout:      15 * time.Second,
			IdleTimeout:       60 * time.Second,
		})
		return
	}
	demo(a)
}

func demo(a *api) {
	ts := httptest.NewServer(a.routes())
	defer ts.Close()

	// call sends a request and prints status, Location (if any) and the body.
	call := func(label, method, path, body string) string {
		var rdr io.Reader
		if body != "" {
			rdr = strings.NewReader(body)
		}
		req, _ := http.NewRequest(method, ts.URL+path, rdr)
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			log.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		fmt.Printf("\n--- %s\n%s %s -> %s\n", label, method, path, resp.Status)
		if loc := resp.Header.Get("Location"); loc != "" {
			fmt.Println("Location:", loc)
		}
		if len(b) > 0 {
			fmt.Print(string(b))
		}
		return resp.Header.Get("Location")
	}

	loc := call("create", "POST", "/tasks", `{"title":"learn net/http","priority":2}`)
	call("create second", "POST", "/tasks", `{"title":"write tests"}`)
	call("list", "GET", "/tasks", "")
	call("get one", "GET", loc, "")
	call("patch", "PATCH", loc, `{"done":true}`)
	call("list only done", "GET", "/tasks?done=true", "")
	call("validation error (422)", "POST", "/tasks", `{"title":"  ","priority":9}`)
	call("unknown field (400)", "POST", "/tasks", `{"titel":"typo"}`)
	call("malformed JSON (400)", "POST", "/tasks", `{"title":`)
	call("duplicate (409)", "POST", "/tasks", `{"title":"LEARN NET/HTTP"}`)
	call("too large (413)", "POST", "/tasks", `{"title":"`+strings.Repeat("x", maxBodyBytes)+`"}`)
	call("delete (204)", "DELETE", loc, "")
	call("get deleted (404)", "GET", loc, "")
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
