// Package handler is the HTTP layer: parse the request, call the service, write the response.
// It is the ONLY package that knows about net/http status codes and JSON field names.
package handler

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"time"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/domain"
	"github.com/vidyesh95/Go-Tutorial/04-web-stdlib/08structure/internal/service"
)

// UserService is the handler's view of the business layer, again declared at the consumer.
// Tests pass a fake; production passes *service.Users.
type UserService interface {
	Register(ctx context.Context, in service.RegisterInput) (domain.User, error)
	Get(ctx context.Context, id uuid.UUID) (domain.User, error)
	List(ctx context.Context) ([]domain.User, error)
}

type Handler struct {
	users  UserService
	logger *slog.Logger
}

func New(users UserService, logger *slog.Logger) *Handler {
	return &Handler{users: users, logger: logger}
}

// Routes returns the handler's routes. main decides where to mount them and which
// middleware (lesson 04) to wrap around them.
func (h *Handler) Routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /users", h.register)
	mux.HandleFunc("GET /users", h.list)
	mux.HandleFunc("GET /users/{id}", h.get)
	return mux
}

// ---------- wire format (DTOs) ----------

// userJSON is the API contract. It is separate from domain.User so that renaming a Go field
// or changing storage never silently changes the public API (and vice versa).
type userJSON struct {
	ID        string    `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
}

func toJSON(u domain.User) userJSON {
	return userJSON{ID: u.ID.String(), Email: u.Email, Name: u.Name, CreatedAt: u.CreatedAt}
}

type registerRequest struct {
	Email string `json:"email"`
	Name  string `json:"name"`
}

// ---------- endpoints ----------

func (h *Handler) register(w http.ResponseWriter, r *http.Request) {
	var req registerRequest
	if !decode(w, r, &req) {
		return
	}
	u, err := h.users.Register(r.Context(), service.RegisterInput{Email: req.Email, Name: req.Name})
	if err != nil {
		h.fail(w, r, err)
		return
	}
	w.Header().Set("Location", "/users/"+u.ID.String())
	respond(w, http.StatusCreated, toJSON(u))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) {
	id, err := uuid.Parse(r.PathValue("id"))
	if err != nil {
		h.fail(w, r, domain.ErrNotFound) // a malformed id cannot exist
		return
	}
	u, err := h.users.Get(r.Context(), id)
	if err != nil {
		h.fail(w, r, err)
		return
	}
	respond(w, http.StatusOK, toJSON(u))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	users, err := h.users.List(r.Context())
	if err != nil {
		h.fail(w, r, err)
		return
	}
	out := make([]userJSON, 0, len(users)) // non-nil: encodes as [] not null
	for _, u := range users {
		out = append(out, toJSON(u))
	}
	respond(w, http.StatusOK, map[string]any{"users": out})
}

// ---------- the ONE place domain errors become HTTP ----------

func (h *Handler) fail(w http.ResponseWriter, r *http.Request, err error) {
	if ve, ok := errors.AsType[*domain.ValidationError](err); ok {
		respondError(w, http.StatusUnprocessableEntity, "validation_failed", "one or more fields are invalid", ve.Fields)
		return
	}
	switch {
	case errors.Is(err, domain.ErrNotFound):
		respondError(w, http.StatusNotFound, "not_found", "user not found", nil)
	case errors.Is(err, domain.ErrEmailTaken):
		respondError(w, http.StatusConflict, "email_taken", "a user with this email already exists", nil)
	default:
		// Unknown error: log the real cause (with the request for correlation), tell the
		// client nothing specific. Leaking internals (SQL text, file paths) helps attackers.
		h.logger.ErrorContext(r.Context(), "unhandled error", "method", r.Method, "path", r.URL.Path, "err", err)
		respondError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
	}
}

// ---------- tiny JSON helpers (see 03-json-api for the long-form explanation) ----------

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		status, code := http.StatusBadRequest, "bad_request"
		if _, ok := errors.AsType[*http.MaxBytesError](err); ok {
			status, code = http.StatusRequestEntityTooLarge, "body_too_large"
		}
		msg := "invalid request body: " + err.Error()
		if errors.Is(err, io.EOF) {
			msg = "request body is empty"
		}
		respondError(w, status, code, msg, nil)
		return false
	}
	return true
}

func respond(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		status, body = http.StatusInternalServerError, []byte(`{"error":{"code":"internal","message":"internal server error"}}`)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	w.Write(append(body, '\n'))
}

func respondError(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	type body struct {
		Code    string            `json:"code"`
		Message string            `json:"message"`
		Fields  map[string]string `json:"fields,omitempty"`
	}
	respond(w, status, map[string]body{"error": {Code: code, Message: msg, Fields: fields}})
}
