// Command demo runs the WHOLE system in one process and walks through a scripted scenario:
//
//	client --HTTP--> gateway --HTTP--> orders-svc --gRPC--> users-svc
//	                                       |
//	                                       +--(outbox relay)--> event bus
//
// Each service listens on its own random localhost port (net.Listen ":0") and has its own
// SQLite database in a temp directory, exactly as if they were separate containers; only the
// process is shared, so a single `go run` shows everything. See README.md for the docker-compose
// version with real separate processes.
//
//	go run ./07-microservices/cmd/demo        # add -v to see every service's logs
//
// The script: create a user (gRPC), place an order through the gateway, replay the same
// Idempotency-Key, reuse the key with a different body, order for an unknown user, show the
// outbox events and the distributed trace, then STOP users-svc and watch orders degrade to 503.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"

	"github.com/vidyesh95/Go-Tutorial/07-microservices/gateway/gatewaysvc"
	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/orders-svc/orderssvc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/users-svc/userssvc"
)

func main() {
	verbose := flag.Bool("v", false, "show every service's structured logs on stderr")
	flag.Parse()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, 60*time.Second) // a demo must never hang
	defer cancel()

	if err := run(ctx, *verbose); err != nil {
		fmt.Fprintln(os.Stderr, "demo failed:", err)
		os.Exit(1)
	}
}

// runningService is one service started in a goroutine that we can stop individually.
type runningService struct {
	name   string
	cancel context.CancelFunc
	done   chan error
}

func start(name string, run func(ctx context.Context) error) *runningService {
	ctx, cancel := context.WithCancel(context.Background())
	s := &runningService{name: name, cancel: cancel, done: make(chan error, 1)}
	go func() { s.done <- run(ctx) }()
	return s
}

// stop asks the service to shut down gracefully and waits for it.
func (s *runningService) stop() error {
	s.cancel()
	select {
	case err := <-s.done:
		s.done <- err // keep the result available for a second stop() call
		return err
	case <-time.After(15 * time.Second):
		return fmt.Errorf("%s did not stop in time", s.name)
	}
}

// demoBus is an EventBus that remembers what was published, so the script can show it.
type demoBus struct {
	mu     sync.Mutex
	events []orderssvc.Event
}

func (b *demoBus) Publish(_ context.Context, e orderssvc.Event) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.events = append(b.events, e)
	return nil
}

func (b *demoBus) snapshot() []orderssvc.Event {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.events)
}

func run(ctx context.Context, verbose bool) error {
	dir, err := os.MkdirTemp("", "microservices-demo-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	logOut := io.Discard
	if verbose {
		logOut = os.Stderr
	}
	log := func(name string) *slog.Logger {
		return slog.New(slog.NewTextHandler(logOut, nil)).With("service", name)
	}

	// --- tracing: every service gets its own provider (so spans carry the right service.name)
	// but they all export into ONE in-memory store, standing in for Jaeger/Tempo.
	spans := tracetest.NewInMemoryExporter()
	tpUsers := telemetry.NewProvider("users-svc", spans)
	tpOrders := telemetry.NewProvider("orders-svc", spans)
	tpGateway := telemetry.NewProvider("gateway", spans)
	tpClient := telemetry.NewProvider("demo-client", spans)
	defer func() {
		for _, tp := range []interface{ Shutdown(context.Context) error }{tpUsers, tpOrders, tpGateway, tpClient} {
			tp.Shutdown(context.Background())
		}
	}()

	// --- listeners on random free ports
	listen := func() (net.Listener, error) { return net.Listen("tcp", "127.0.0.1:0") }
	usersLn, err := listen()
	if err != nil {
		return err
	}
	ordersLn, err := listen()
	if err != nil {
		return err
	}
	gatewayLn, err := listen()
	if err != nil {
		return err
	}
	usersAddr := usersLn.Addr().String()
	gatewayURL := "http://" + gatewayLn.Addr().String()
	ordersURL, _ := url.Parse("http://" + ordersLn.Addr().String())

	fmt.Println("=== Starting three services (each with its own SQLite database) ===")
	fmt.Printf("users-svc   gRPC  %s\norders-svc  HTTP  %s\ngateway     HTTP  %s\n",
		usersAddr, ordersLn.Addr(), gatewayLn.Addr())

	bus := &demoBus{}
	usersSvc := start("users-svc", func(ctx context.Context) error {
		return userssvc.Run(ctx, usersLn, userssvc.Config{DBPath: dir + "/users.db"}, log("users-svc"), tpUsers)
	})
	ordersSvc := start("orders-svc", func(ctx context.Context) error {
		return orderssvc.Run(ctx, ordersLn, orderssvc.Config{
			DBPath:        dir + "/orders.db",
			UsersAddr:     usersAddr,
			RelayInterval: 50 * time.Millisecond,
			Bus:           bus,
		}, log("orders-svc"), tpOrders)
	})
	gatewaySvc := start("gateway", func(ctx context.Context) error {
		cfg := gatewaysvc.Config{Routes: []gatewaysvc.Route{{Name: "orders", Target: ordersURL, Timeout: 3 * time.Second}}}
		return gatewaysvc.Run(ctx, gatewayLn, cfg, log("gateway"), tpGateway)
	})
	services := []*runningService{gatewaySvc, ordersSvc, usersSvc} // stop in this order: edge first
	shutdown := func() error {
		var errs []error
		for _, s := range services {
			errs = append(errs, s.stop())
		}
		return errors.Join(errs...)
	}
	defer shutdown() // safety net if the script fails midway; stop() is idempotent

	// --- wait until ready
	if err := waitReady(ctx, usersAddr, gatewayURL); err != nil {
		return err
	}
	fmt.Println("all services report healthy")
	fmt.Println()

	// The client side of the scenario: its HTTP calls carry a trace context, so the whole
	// request tree below belongs to one trace that STARTS here.
	httpc := &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport,
		otelhttp.WithTracerProvider(tpClient), otelhttp.WithPropagators(telemetry.Propagator()))}

	// --- 1. create a user directly over gRPC --------------------------------------------------
	fmt.Println("=== 1. Create a user over gRPC (users-svc) ===")
	conn, err := grpc.NewClient(usersAddr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	usersClient := usersv1.NewUsersServiceClient(conn)
	created, err := usersClient.CreateUser(ctx, &usersv1.CreateUserRequest{Name: "Ada Lovelace", Email: "ada@example.com"})
	if err != nil {
		return err
	}
	userID := created.GetUser().GetId()
	fmt.Printf("created user %s (%s)\n", userID, created.GetUser().GetEmail())
	_, err = usersClient.CreateUser(ctx, &usersv1.CreateUserRequest{Name: "Impostor", Email: "ada@example.com"})
	fmt.Printf("same email again  -> gRPC code %s\n", status.Code(err))
	_, err = usersClient.GetUser(ctx, &usersv1.GetUserRequest{Id: "no-such-user"})
	fmt.Printf("unknown user id   -> gRPC code %s\n\n", status.Code(err))

	// --- 2. place an order through the gateway -------------------------------------------------
	fmt.Println("=== 2. Place an order through the gateway (HTTP -> gateway -> orders-svc -> gRPC users-svc) ===")
	traceCtx, rootSpan := tpClient.Tracer("demo").Start(ctx, "demo: place order")
	orderBody := fmt.Sprintf(`{"user_id":%q,"item":"Go in Action","quantity":2}`, userID)
	const key = "order-req-0001"
	resp1, err := postOrder(traceCtx, httpc, gatewayURL, key, orderBody)
	rootSpan.End()
	if err != nil {
		return err
	}
	resp1.print("POST /api/orders  Idempotency-Key: " + key)
	traceID := rootSpan.SpanContext().TraceID()

	// --- 3. replay -----------------------------------------------------------------------------
	fmt.Println("\n=== 3. Retry with the SAME Idempotency-Key (a client that timed out and tries again) ===")
	resp2, err := postOrder(ctx, httpc, gatewayURL, key, orderBody)
	if err != nil {
		return err
	}
	resp2.print("POST /api/orders  Idempotency-Key: " + key)
	fmt.Printf("same order id returned: %v (no duplicate order, no second users-svc call)\n", resp1.field("id") == resp2.field("id"))

	fmt.Println("\n=== 3b. Same key, DIFFERENT body (a client bug) ===")
	resp3, err := postOrder(ctx, httpc, gatewayURL, key, fmt.Sprintf(`{"user_id":%q,"item":"Go in Action","quantity":99}`, userID))
	if err != nil {
		return err
	}
	resp3.print("POST /api/orders  Idempotency-Key: " + key)

	// --- 4. unknown user -----------------------------------------------------------------------
	fmt.Println("\n=== 4. Order for a user that does not exist (gRPC NotFound -> HTTP 422) ===")
	resp4, err := postOrder(ctx, httpc, gatewayURL, "order-req-0002", `{"user_id":"ghost","item":"Ghost Book","quantity":1}`)
	if err != nil {
		return err
	}
	resp4.print("POST /api/orders  Idempotency-Key: order-req-0002")

	fmt.Println("\n=== 5. Read the order back (GET /api/orders/{id}) ===")
	got, err := httpc.Get(gatewayURL + "/api/orders/" + resp1.field("id"))
	if err != nil {
		return err
	}
	read(got).print("GET /api/orders/" + resp1.field("id"))

	// --- 6. outbox -----------------------------------------------------------------------------
	fmt.Println("\n=== 6. Transactional outbox: events the relay delivered to the bus ===")
	if err := waitFor(ctx, func() bool { return len(bus.snapshot()) >= 1 }); err != nil {
		return fmt.Errorf("outbox relay never published: %w", err)
	}
	time.Sleep(200 * time.Millisecond) // several more relay polls: a duplicate event would show up now
	for _, e := range bus.snapshot() {
		fmt.Printf("event %s topic=%s payload=%s\n", e.ID, e.Topic, e.Payload)
	}
	fmt.Printf("%d events for 4 requests: only the one real order produced an event\n", len(bus.snapshot()))

	// --- 7. trace ------------------------------------------------------------------------------
	fmt.Println("\n=== 7. Distributed trace of step 2 (one trace ID across client, gateway, orders-svc and users-svc) ===")
	for _, tp := range []interface{ ForceFlush(context.Context) error }{tpClient, tpGateway, tpOrders, tpUsers} {
		tp.ForceFlush(ctx)
	}
	printTrace(spans.GetSpans(), traceID)

	// --- 8. users-svc dies ---------------------------------------------------------------------
	fmt.Println("\n=== 8. Stop users-svc, then try to place another order ===")
	if err := usersSvc.stop(); err != nil {
		return fmt.Errorf("stop users-svc: %w", err)
	}
	fmt.Println("users-svc stopped")
	startedAt := time.Now()
	resp5, err := postOrder(ctx, httpc, gatewayURL, "order-req-0003", orderBody)
	if err != nil {
		return err
	}
	resp5.print("POST /api/orders  Idempotency-Key: order-req-0003")
	fmt.Printf("took %s: orders-svc made 3 gRPC attempts with backoff between them (Unavailable is retried), then gave up\n",
		time.Since(startedAt).Round(10*time.Millisecond))

	fmt.Println("\n=== 8b. The gateway still answers; /healthz reports each downstream ===")
	hz, err := httpc.Get(gatewayURL + "/healthz")
	if err != nil {
		return err
	}
	read(hz).print("GET /healthz")
	fmt.Println("(orders-svc stays 'ok': its readiness checks only its own database. users-svc being down")
	fmt.Println(" shows up as 503s on the routes that need it, not as every service flipping to unhealthy.)")

	fmt.Println("\n=== 8c. Replaying the FIRST order's key still works: it is answered from orders-svc's own database ===")
	resp6, err := postOrder(ctx, httpc, gatewayURL, key, orderBody)
	if err != nil {
		return err
	}
	resp6.print("POST /api/orders  Idempotency-Key: " + key)

	// --- shutdown ------------------------------------------------------------------------------
	fmt.Println("\n=== Shutting everything down gracefully ===")
	if err := shutdown(); err != nil {
		return err
	}
	fmt.Println("done")
	return nil
}

// --- small HTTP helpers ---------------------------------------------------------------------

// result is one HTTP response, read fully so it can be printed and queried.
type result struct {
	status    int
	requestID string
	replayed  string
	body      []byte
}

func read(resp *http.Response) result {
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return result{
		status:    resp.StatusCode,
		requestID: resp.Header.Get("X-Request-ID"),
		replayed:  resp.Header.Get("Idempotent-Replayed"),
		body:      bytes.TrimSpace(b),
	}
}

func (r result) print(what string) {
	extra := ""
	if r.replayed != "" {
		extra = "  Idempotent-Replayed: " + r.replayed
	}
	fmt.Printf("%s\n  -> %d%s  X-Request-ID: %s\n  %s\n", what, r.status, extra, r.requestID, r.body)
}

// field returns a top-level string field from the JSON body ("" if absent).
func (r result) field(name string) string {
	var m map[string]any
	if json.Unmarshal(r.body, &m) != nil {
		return ""
	}
	s, _ := m[name].(string)
	return s
}

func postOrder(ctx context.Context, c *http.Client, base, key, body string) (result, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/orders", strings.NewReader(body))
	if err != nil {
		return result{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Idempotency-Key", key)
	resp, err := c.Do(req)
	if err != nil {
		return result{}, err
	}
	return read(resp), nil
}

// waitReady polls until users-svc (gRPC health) and the gateway (/healthz, which checks
// orders-svc) are all healthy.
func waitReady(ctx context.Context, usersAddr, gatewayURL string) error {
	return waitFor(ctx, func() bool {
		if userssvc.Healthcheck(ctx, usersAddr) != nil {
			return false
		}
		resp, err := http.Get(gatewayURL + "/healthz")
		if err != nil {
			return false
		}
		resp.Body.Close()
		return resp.StatusCode == http.StatusOK
	})
}

// waitFor polls cond every 20ms until it is true, ctx ends, or 10 seconds pass.
func waitFor(ctx context.Context, cond func() bool) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	for !cond() {
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting: %w", ctx.Err())
		case <-time.After(20 * time.Millisecond):
		}
	}
	return nil
}

// printTrace prints the span tree of one trace: who did what, nested by parent/child.
func printTrace(all tracetest.SpanStubs, id trace.TraceID) {
	var mine tracetest.SpanStubs
	for _, s := range all {
		if s.SpanContext.TraceID() == id {
			mine = append(mine, s)
		}
	}
	slices.SortFunc(mine, func(a, b tracetest.SpanStub) int { return a.StartTime.Compare(b.StartTime) })

	present := make(map[trace.SpanID]bool, len(mine))
	children := make(map[trace.SpanID]tracetest.SpanStubs)
	for _, s := range mine {
		present[s.SpanContext.SpanID()] = true
	}
	var roots tracetest.SpanStubs
	for _, s := range mine {
		if p := s.Parent.SpanID(); present[p] {
			children[p] = append(children[p], s)
		} else {
			roots = append(roots, s)
		}
	}

	fmt.Printf("trace %s: %d spans\n", id, len(mine))
	var walk func(s tracetest.SpanStub, depth int)
	walk = func(s tracetest.SpanStub, depth int) {
		service := "?"
		if v, ok := s.Resource.Set().Value("service.name"); ok {
			service = v.AsString()
		}
		fmt.Printf("  %s%-12s %-8s %-36s %s\n", strings.Repeat("  ", depth), service,
			s.SpanKind.String(), s.Name, s.EndTime.Sub(s.StartTime).Round(10*time.Microsecond))
		for _, c := range children[s.SpanContext.SpanID()] {
			walk(c, depth+1)
		}
	}
	for _, r := range roots {
		walk(r, 0)
	}
}
