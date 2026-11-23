package orderssvc

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/sqlitedb"
)

var discard = slog.New(slog.NewTextHandler(io.Discard, nil))

// fakeUsers stands in for users-svc: no gRPC, no network, answers programmed per test.
// This is why orders depends on the UsersClient INTERFACE.
type fakeUsers struct {
	calls atomic.Int32
	err   error // returned for every call when set
}

func (f *fakeUsers) GetUser(_ context.Context, id string) (User, error) {
	f.calls.Add(1)
	if f.err != nil {
		return User{}, f.err
	}
	return User{ID: id, Name: "Test"}, nil
}

func newStore(t *testing.T) *Store {
	t.Helper()
	db, err := sqlitedb.Open(t.Context(), filepath.Join(t.TempDir(), "orders.db"), Schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func newHandler(t *testing.T, users UsersClient) (http.Handler, *Store) {
	t.Helper()
	st := newStore(t)
	return NewHandler(st, users, discard, nil), st
}

const body = `{"user_id":"u1","item":"book","quantity":2}`

func post(h http.Handler, key, payload string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/orders", strings.NewReader(payload))
	if key != "" {
		req.Header.Set(IdempotencyKeyHeader, key)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func decodeOrder(t *testing.T, rec *httptest.ResponseRecorder) Order {
	t.Helper()
	var o Order
	if err := json.Unmarshal(rec.Body.Bytes(), &o); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return o
}

func TestCreateOrder(t *testing.T) {
	users := &fakeUsers{}
	h, st := newHandler(t, users)

	rec := post(h, "key-1", body)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, body %s", rec.Code, rec.Body)
	}
	o := decodeOrder(t, rec)
	if o.ID == "" || o.UserID != "u1" || o.Quantity != 2 || o.Status != "created" {
		t.Fatalf("unexpected order %+v", o)
	}

	// GET returns what was stored.
	req := httptest.NewRequest(http.MethodGet, "/orders/"+o.ID, nil)
	get := httptest.NewRecorder()
	h.ServeHTTP(get, req)
	if get.Code != http.StatusOK || decodeOrder(t, get).ID != o.ID {
		t.Fatalf("GET = %d %s", get.Code, get.Body)
	}
	req = httptest.NewRequest(http.MethodGet, "/orders/missing", nil)
	get = httptest.NewRecorder()
	h.ServeHTTP(get, req)
	if get.Code != http.StatusNotFound {
		t.Fatalf("GET missing = %d", get.Code)
	}

	// The outbox event was written together with the order.
	if sent, unsent, err := st.CountOutbox(t.Context()); err != nil || sent != 0 || unsent != 1 {
		t.Fatalf("outbox sent=%d unsent=%d err=%v, want 0/1", sent, unsent, err)
	}
}

func TestIdempotencyReplay(t *testing.T) {
	users := &fakeUsers{}
	h, st := newHandler(t, users)

	first := post(h, "key-1", body)
	// Same key, same data, different whitespace/key order: still "the same request".
	second := post(h, "key-1", `{ "quantity": 2, "item": "book", "user_id": "u1" }`)

	if first.Code != http.StatusCreated || second.Code != http.StatusOK {
		t.Fatalf("statuses = %d, %d; want 201, 200", first.Code, second.Code)
	}
	if second.Header().Get("Idempotent-Replayed") != "true" {
		t.Error("replay response is missing Idempotent-Replayed: true")
	}
	if a, b := decodeOrder(t, first), decodeOrder(t, second); a != b {
		t.Fatalf("replay returned a different order: %+v vs %+v", a, b)
	}
	if n := users.calls.Load(); n != 1 {
		t.Errorf("users-svc called %d times, want 1 (the replay must not call it)", n)
	}
	// Exactly one order and one event despite two requests.
	if _, unsent, _ := st.CountOutbox(t.Context()); unsent != 1 {
		t.Errorf("outbox has %d events, want 1", unsent)
	}
}

func TestIdempotencyKeyReuseWithDifferentBody(t *testing.T) {
	h, _ := newHandler(t, &fakeUsers{})
	post(h, "key-1", body)
	rec := post(h, "key-1", `{"user_id":"u1","item":"book","quantity":3}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want 422", rec.Code)
	}
}

func TestReplayStillWorksWhenUsersServiceIsDown(t *testing.T) {
	users := &fakeUsers{}
	h, _ := newHandler(t, users)
	post(h, "key-1", body)

	users.err = ErrUsersUnavailable
	if rec := post(h, "key-1", body); rec.Code != http.StatusOK {
		t.Fatalf("replay status = %d, want 200", rec.Code)
	}
	// ...but a NEW key needs the users service, so it fails.
	if rec := post(h, "key-2", body); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("new-key status = %d, want 503", rec.Code)
	}
}

func TestConcurrentSameKeyCreatesOneOrder(t *testing.T) {
	h, st := newHandler(t, &fakeUsers{})
	var wg sync.WaitGroup
	ids := make([]string, 8)
	for i := range ids {
		wg.Go(func() { ids[i] = decodeOrder(t, post(h, "same-key", body)).ID })
	}
	wg.Wait()
	for _, id := range ids {
		if id != ids[0] || id == "" {
			t.Fatalf("concurrent requests produced different orders: %v", ids)
		}
	}
	if _, unsent, _ := st.CountOutbox(t.Context()); unsent != 1 {
		t.Fatalf("outbox has %d events, want 1", unsent)
	}
}

func TestErrorMapping(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		payload string
		users   error
		want    int
	}{
		{"missing idempotency key", "", body, nil, http.StatusBadRequest},
		{"bad json", "k", `{`, nil, http.StatusBadRequest},
		{"unknown field", "k", `{"user_id":"u1","item":"x","quantity":1,"extra":1}`, nil, http.StatusBadRequest},
		{"zero quantity", "k", `{"user_id":"u1","item":"x","quantity":0}`, nil, http.StatusBadRequest},
		{"unknown user", "k", body, ErrUserNotFound, http.StatusUnprocessableEntity},
		{"users unavailable", "k", body, ErrUsersUnavailable, http.StatusServiceUnavailable},
		{"users unavailable (wrapped)", "k", body, errors.Join(ErrUsersUnavailable, context.DeadlineExceeded), http.StatusServiceUnavailable},
		{"unexpected error", "k", body, errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, st := newHandler(t, &fakeUsers{err: tt.users})
			rec := post(h, tt.key, tt.payload)
			if rec.Code != tt.want {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.want, rec.Body)
			}
			// A failed request must leave nothing behind: no order, no event.
			if sent, unsent, _ := st.CountOutbox(t.Context()); sent+unsent != 0 {
				t.Errorf("failed request wrote %d outbox events", sent+unsent)
			}
		})
	}
}

// --- outbox relay -----------------------------------------------------------------------------

type recordingBus struct {
	mu     sync.Mutex
	events []Event
	fail   bool
}

func (b *recordingBus) Publish(_ context.Context, e Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.fail {
		return errors.New("broker down")
	}
	b.events = append(b.events, e)
	return nil
}

func TestRelayPublishesAndMarksSent(t *testing.T) {
	h, st := newHandler(t, &fakeUsers{})
	post(h, "k1", body)
	post(h, "k2", body)

	bus := &recordingBus{}
	relay := &Relay{Store: st, Bus: bus, Log: discard, Interval: time.Hour}
	n, err := relay.Poll(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("Poll = %d, %v; want 2, nil", n, err)
	}
	if len(bus.events) != 2 || bus.events[0].Topic != TopicOrderCreated {
		t.Fatalf("published %+v", bus.events)
	}
	if !strings.Contains(string(bus.events[0].Payload), `"user_id":"u1"`) {
		t.Errorf("payload = %s", bus.events[0].Payload)
	}
	if sent, unsent, _ := st.CountOutbox(t.Context()); sent != 2 || unsent != 0 {
		t.Fatalf("outbox sent=%d unsent=%d, want 2/0", sent, unsent)
	}
	// Nothing left to do on the next poll: events are not published twice in normal operation.
	if n, _ := relay.Poll(t.Context()); n != 0 {
		t.Fatalf("second Poll published %d events", n)
	}
}

func TestRelayKeepsEventsWhenBrokerIsDown(t *testing.T) {
	h, st := newHandler(t, &fakeUsers{})
	post(h, "k1", body)

	bus := &recordingBus{fail: true}
	relay := &Relay{Store: st, Bus: bus, Log: discard, Interval: time.Hour}
	if _, err := relay.Poll(t.Context()); err == nil {
		t.Fatal("expected an error while the broker is down")
	}
	if _, unsent, _ := st.CountOutbox(t.Context()); unsent != 1 {
		t.Fatalf("event was lost: unsent=%d, want 1", unsent)
	}

	bus.fail = false // broker recovers; the event is delivered later: nothing was lost
	if n, err := relay.Poll(t.Context()); err != nil || n != 1 {
		t.Fatalf("Poll after recovery = %d, %v", n, err)
	}
}

func TestRelayRunStopsOnContextCancel(t *testing.T) {
	h, st := newHandler(t, &fakeUsers{})
	post(h, "k1", body)
	bus := &recordingBus{}
	relay := &Relay{Store: st, Bus: bus, Log: discard, Interval: 10 * time.Millisecond}

	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { relay.Run(ctx); close(done) }()

	deadline := time.Now().Add(5 * time.Second)
	for {
		bus.mu.Lock()
		n := len(bus.events)
		bus.mu.Unlock()
		if n == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("relay never published the event")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("relay did not stop after cancel")
	}
}

// --- gRPC client: deadlines and retries -------------------------------------------------------

// fakeGRPC is a usersv1.UsersServiceClient that returns a scripted sequence of results.
type fakeGRPC struct {
	usersv1.UsersServiceClient // nil: CreateUser is never called and would panic if it were
	script                     []error
	calls                      int
	sawDeadline                bool
}

func (f *fakeGRPC) GetUser(ctx context.Context, in *usersv1.GetUserRequest, _ ...grpc.CallOption) (*usersv1.GetUserResponse, error) {
	_, hasDeadline := ctx.Deadline()
	f.sawDeadline = hasDeadline
	err := f.script[min(f.calls, len(f.script)-1)]
	f.calls++
	if err != nil {
		return nil, err
	}
	return &usersv1.GetUserResponse{User: &usersv1.User{Id: in.GetId(), Name: "Ada"}}, nil
}

func newGRPCUsers(f *fakeGRPC) *GRPCUsers {
	return &GRPCUsers{Client: f, Timeout: time.Second, MaxAttempts: 3, BaseBackoff: time.Millisecond}
}

func TestGRPCUsersRetriesOnlyUnavailable(t *testing.T) {
	unavailable := status.Error(codes.Unavailable, "down")
	tests := []struct {
		name      string
		script    []error
		wantCalls int
		wantErr   error // nil = success
	}{
		{"success first try", []error{nil}, 1, nil},
		{"recovers after two Unavailable", []error{unavailable, unavailable, nil}, 3, nil},
		{"gives up after max attempts", []error{unavailable}, 3, ErrUsersUnavailable},
		{"NotFound is not retried", []error{status.Error(codes.NotFound, "x")}, 1, ErrUserNotFound},
		{"DeadlineExceeded is not retried", []error{status.Error(codes.DeadlineExceeded, "slow")}, 1, ErrUsersUnavailable},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeGRPC{script: tt.script}
			u, err := newGRPCUsers(f).GetUser(t.Context(), "u1")
			if f.calls != tt.wantCalls {
				t.Errorf("calls = %d, want %d", f.calls, tt.wantCalls)
			}
			if !f.sawDeadline {
				t.Error("call had no deadline")
			}
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr == nil && u.ID != "u1" {
				t.Errorf("user = %+v", u)
			}
		})
	}
}

func TestGRPCUsersStopsRetryingWhenContextCancelled(t *testing.T) {
	f := &fakeGRPC{script: []error{status.Error(codes.Unavailable, "down")}}
	g := newGRPCUsers(f)
	g.BaseBackoff = time.Hour // would hang for an hour if the wait ignored ctx
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if _, err := g.GetUser(ctx, "u1"); !errors.Is(err, ErrUsersUnavailable) {
		t.Fatalf("err = %v", err)
	}
}
