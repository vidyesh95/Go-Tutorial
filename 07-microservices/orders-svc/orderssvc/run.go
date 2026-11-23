package orderssvc

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/sqlitedb"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/svc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
)

// Config is what Run needs from the outside world.
type Config struct {
	DBPath        string
	UsersAddr     string        // host:port of users-svc's gRPC endpoint
	CallTimeout   time.Duration // per-attempt deadline for calls to users-svc (default 1s)
	MaxAttempts   int           // total tries per call (default 3)
	BaseBackoff   time.Duration // first retry wait (default 50ms)
	RelayInterval time.Duration // outbox poll interval (default 200ms)
	Bus           EventBus      // where outbox events go (default: log them)
}

func (c *Config) setDefaults(log *slog.Logger) {
	if c.CallTimeout == 0 {
		c.CallTimeout = time.Second
	}
	if c.MaxAttempts == 0 {
		c.MaxAttempts = 3
	}
	if c.BaseBackoff == 0 {
		c.BaseBackoff = 50 * time.Millisecond
	}
	if c.RelayInterval == 0 {
		c.RelayInterval = 200 * time.Millisecond
	}
	if c.Bus == nil {
		c.Bus = LogBus{Log: log}
	}
}

// Run serves the orders API on ln and runs the outbox relay until ctx is cancelled, then shuts
// down in dependency order: stop accepting requests, stop the relay, close the gRPC connection,
// close the database.
func Run(ctx context.Context, ln net.Listener, cfg Config, log *slog.Logger, tp trace.TracerProvider) error {
	cfg.setDefaults(log)
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}

	db, err := sqlitedb.Open(ctx, cfg.DBPath, Schema)
	if err != nil {
		return err
	}
	defer db.Close()
	store := NewStore(db)

	// grpc.NewClient does not connect yet: it connects lazily on the first RPC and reconnects
	// automatically with backoff, so orders-svc can start before users-svc is up.
	conn, err := grpc.NewClient(cfg.UsersAddr,
		grpc.WithTransportCredentials(insecure.NewCredentials()), // plaintext: fine in a private network; use mTLS in production
		// The client stats handler creates a client span per RPC and INJECTS the current trace
		// context into the gRPC metadata, so users-svc's span becomes a child of ours.
		grpc.WithStatsHandler(otelgrpc.NewClientHandler(
			otelgrpc.WithTracerProvider(tp),
			otelgrpc.WithPropagators(telemetry.Propagator()),
		)),
	)
	if err != nil {
		return err
	}
	defer conn.Close()
	users := &GRPCUsers{
		Client:      usersv1.NewUsersServiceClient(conn),
		Timeout:     cfg.CallTimeout,
		MaxAttempts: cfg.MaxAttempts,
		BaseBackoff: cfg.BaseBackoff,
	}

	relay := &Relay{Store: store, Bus: cfg.Bus, Log: log, Interval: cfg.RelayInterval}
	// The relay gets its own cancelable context so it also stops if the HTTP server fails early
	// (ctx alone would stay live and wg.Wait below would block forever).
	relayCtx, stopRelay := context.WithCancel(ctx)
	var wg sync.WaitGroup
	wg.Go(func() { relay.Run(relayCtx) }) // Go 1.25: WaitGroup.Go
	// Deferred calls run last-in-first-out: stop the relay, wait for it, THEN conn.Close and
	// db.Close (deferred earlier) run, so the relay can never touch a closed database.
	defer wg.Wait()
	defer stopRelay()

	log.Info("orders-svc listening", "addr", ln.Addr().String(), "users", cfg.UsersAddr)
	return svc.ServeHTTP(ctx, ln, NewHandler(store, users, log, tp), log)
}
