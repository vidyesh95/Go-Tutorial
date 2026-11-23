// Package orderssvc is the orders microservice: an HTTP/JSON API backed by its own SQLite
// database. To create an order it must know the user exists, so it makes a synchronous gRPC
// call to users-svc; and once the order is saved it announces it asynchronously through a
// transactional outbox.
//
// In industry: this mix is typical. Use synchronous calls (gRPC/HTTP) when you need an answer
// NOW to decide something ("is this user real?"), and asynchronous events when others merely
// need to REACT to something that happened ("send a confirmation email").
package orderssvc

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/svc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
)

// IdempotencyKeyHeader is the request header clients send on POST /orders.
const IdempotencyKeyHeader = "Idempotency-Key"

// API holds the HTTP handlers' dependencies.
type API struct {
	store *Store
	users UsersClient
	log   *slog.Logger
}

// NewHandler builds the complete HTTP handler: routes plus middleware plus tracing.
func NewHandler(store *Store, users UsersClient, log *slog.Logger, tp trace.TracerProvider) http.Handler {
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}
	a := &API{store: store, users: users, log: log}
	mux := http.NewServeMux()
	// Go 1.22+ patterns: method + path wildcard in the pattern itself.
	mux.HandleFunc("POST /orders", a.createOrder)
	mux.HandleFunc("GET /orders/{id}", a.getOrder)
	mux.HandleFunc("GET /healthz", a.healthz)

	// Outermost first: the tracing span covers everything inside it, and the request ID is
	// set before the access log reads it.
	return otelhttp.NewHandler(svc.RequestID(svc.AccessLog(log, mux)), "orders-svc",
		otelhttp.WithTracerProvider(tp),
		otelhttp.WithPropagators(telemetry.Propagator()))
}

type createOrderRequest struct {
	UserID   string `json:"user_id"`
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

// createOrder handles POST /orders.
//
// IDEMPOTENCY: networks fail in the middle. A client that sends "create order" and then times
// out does not know whether the order was created. Retrying blindly could charge the customer
// twice. So the client attaches a unique Idempotency-Key and the server guarantees: the same
// key with the same body creates the order at most once, and every repeat returns the original
// result. (Stripe's API popularised this contract.)
func (a *API) createOrder(w http.ResponseWriter, r *http.Request) {
	key := strings.TrimSpace(r.Header.Get(IdempotencyKeyHeader))
	if key == "" || len(key) > 128 {
		svc.WriteError(w, http.StatusBadRequest, IdempotencyKeyHeader+" header is required (1-128 chars)")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // cap request bodies at 1 MiB
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields() // a typo'd field name is an error, not silently ignored
	var req createOrderRequest
	if err := dec.Decode(&req); err != nil {
		svc.WriteError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	if req.UserID == "" || req.Item == "" || req.Quantity < 1 || req.Quantity > 1000 {
		svc.WriteError(w, http.StatusBadRequest, "user_id, item and quantity (1-1000) are required")
		return
	}
	hash := requestHash(req)

	// Fast path: a repeat of a request we already handled. Answer from our own database
	// WITHOUT calling users-svc, so a retry still succeeds while users-svc is down.
	if o, found, err := a.store.Replay(r.Context(), key, hash); err != nil {
		a.fail(w, err)
		return
	} else if found {
		a.writeReplay(w, o)
		return
	}

	// Synchronous call to another service. Deliberately OUTSIDE any database transaction:
	// holding a write lock while waiting on the network would let one slow service freeze
	// every writer.
	if _, err := a.users.GetUser(r.Context(), req.UserID); err != nil {
		a.fail(w, err)
		return
	}

	o, replayed, err := a.store.CreateOrder(r.Context(), key, hash, Order{
		UserID: req.UserID, Item: req.Item, Quantity: req.Quantity,
	})
	if err != nil {
		a.fail(w, err)
		return
	}
	if replayed { // lost a race with a concurrent request carrying the same key
		a.writeReplay(w, o)
		return
	}
	w.Header().Set("Location", "/orders/"+o.ID)
	svc.WriteJSON(w, http.StatusCreated, o)
}

func (a *API) writeReplay(w http.ResponseWriter, o Order) {
	w.Header().Set("Idempotent-Replayed", "true") // lets a client tell "new" from "repeat"
	w.Header().Set("Location", "/orders/"+o.ID)
	svc.WriteJSON(w, http.StatusOK, o)
}

func (a *API) getOrder(w http.ResponseWriter, r *http.Request) {
	o, err := a.store.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		a.fail(w, err)
		return
	}
	svc.WriteJSON(w, http.StatusOK, o)
}

// healthz is a READINESS check on this service's own dependencies only (its database).
// It deliberately does not call users-svc: if readiness depended on downstream health, one
// failing service would flip every upstream service to "not ready" and take the whole system
// out of rotation (a cascading failure). Degrade per request instead (503 on the affected route).
func (a *API) healthz(w http.ResponseWriter, r *http.Request) {
	if err := a.store.Ping(r.Context()); err != nil {
		svc.WriteError(w, http.StatusServiceUnavailable, "database unreachable")
		return
	}
	svc.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// fail is the single place errors become HTTP responses, mirroring toStatus in users-svc.
//
//	unknown user          -> 422 Unprocessable Entity: the request is well-formed but refers to
//	                         something that does not exist. (404 would wrongly suggest that the
//	                         URL /orders is missing.)
//	users-svc down/slow   -> 503 Service Unavailable + Retry-After: our dependency failed, so
//	                         the client may retry later.
func (a *API) fail(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, ErrUserNotFound):
		svc.WriteError(w, http.StatusUnprocessableEntity, "unknown user")
	case errors.Is(err, ErrUsersUnavailable):
		a.log.Warn("users-svc unavailable", "err", err)
		w.Header().Set("Retry-After", "1")
		svc.WriteError(w, http.StatusServiceUnavailable, "users service unavailable, retry later")
	case errors.Is(err, ErrKeyReuse):
		svc.WriteError(w, http.StatusUnprocessableEntity, "Idempotency-Key was already used with a different request body")
	case errors.Is(err, ErrOrderNotFound):
		svc.WriteError(w, http.StatusNotFound, "order not found")
	default:
		a.log.Error("unexpected error", "err", err) // details go to the log, not the client
		svc.WriteError(w, http.StatusInternalServerError, "internal error")
	}
}

// requestHash fingerprints the request. We hash the DECODED struct re-encoded by json.Marshal
// (fixed field order), so two bodies that differ only in whitespace or key order are "the same
// request".
func requestHash(req createOrderRequest) string {
	b, _ := json.Marshal(req) // marshalling a struct of strings and ints cannot fail
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
