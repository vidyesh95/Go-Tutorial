package userssvc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"runtime/debug"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/google.golang.org/grpc/otelgrpc"
	"go.opentelemetry.io/otel/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/health"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/reflection"
	"google.golang.org/grpc/status"

	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/sqlitedb"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
)

// ServiceName is the fully-qualified gRPC service name, also used as the health-check key.
const ServiceName = "users.v1.UsersService"

// NewGRPCServer assembles the gRPC server: interceptors, tracing, the UsersService itself,
// the standard health service and reflection. Tests use it directly with an in-memory listener.
//
// The returned *health.Server lets the caller flip the service to NOT_SERVING during shutdown.
func NewGRPCServer(store *Store, log *slog.Logger, tp trace.TracerProvider) (*grpc.Server, *health.Server) {
	if tp == nil {
		tp = tracenoop.NewTracerProvider()
	}
	srv := grpc.NewServer(
		// The stats handler creates a server span per RPC and extracts the caller's trace
		// context from gRPC metadata, so these spans join the caller's trace.
		grpc.StatsHandler(otelgrpc.NewServerHandler(
			otelgrpc.WithTracerProvider(tp),
			otelgrpc.WithPropagators(telemetry.Propagator()),
		)),
		// Interceptors are gRPC's middleware. The FIRST listed is the outermost: logging
		// wraps recovery so the log line shows the Internal code a panic is turned into.
		grpc.ChainUnaryInterceptor(LoggingInterceptor(log), RecoveryInterceptor(log)),
	)
	usersv1.RegisterUsersServiceServer(srv, NewServer(store))

	// The standard grpc.health.v1 service: load balancers, Kubernetes (native gRPC probes)
	// and our Docker healthcheck ask it whether we are serving. "" means "the whole process".
	hs := health.NewServer()
	hs.SetServingStatus("", healthpb.HealthCheckResponse_SERVING)
	hs.SetServingStatus(ServiceName, healthpb.HealthCheckResponse_SERVING)
	healthpb.RegisterHealthServer(srv, hs)

	// Reflection lets tools like grpcurl discover services without the .proto file. Convenient
	// in dev; many companies disable it on public-facing production endpoints.
	reflection.Register(srv)
	return srv, hs
}

// LoggingInterceptor logs every unary RPC with its method, gRPC code and duration.
func LoggingInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		start := time.Now()
		resp, err := handler(ctx, req)
		log.Info("rpc", "method", info.FullMethod, "code", status.Code(err).String(),
			"duration", time.Since(start).Round(time.Microsecond).String())
		return resp, err
	}
}

// RecoveryInterceptor turns a panic in a handler into codes.Internal instead of crashing the
// whole server (and every other in-flight RPC with it). The stack goes to the log, not the caller.
func RecoveryInterceptor(log *slog.Logger) grpc.UnaryServerInterceptor {
	return func(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (resp any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("panic in rpc", "method", info.FullMethod, "panic", r, "stack", string(debug.Stack()))
				err = status.Error(codes.Internal, "internal error")
			}
		}()
		return handler(ctx, req)
	}
}

// Config is what Run needs from the outside world.
type Config struct {
	DBPath string // SQLite file; this service's private database
}

// Run opens the database, serves gRPC on ln, and blocks until ctx is cancelled, then shuts down
// gracefully: report NOT_SERVING, let in-flight RPCs finish (GracefulStop), close the database.
func Run(ctx context.Context, ln net.Listener, cfg Config, log *slog.Logger, tp trace.TracerProvider) error {
	db, err := sqlitedb.Open(ctx, cfg.DBPath, Schema)
	if err != nil {
		return err
	}
	defer db.Close() // runs last, after the server has stopped using it

	srv, hs := NewGRPCServer(NewStore(db), log, tp)
	errc := make(chan error, 1)
	go func() { errc <- srv.Serve(ln) }()
	log.Info("users-svc listening", "addr", ln.Addr().String())

	select {
	case err := <-errc:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	log.Info("users-svc shutting down")
	hs.Shutdown() // health -> NOT_SERVING so load balancers stop sending new traffic
	stopped := make(chan struct{})
	go func() { srv.GracefulStop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		srv.Stop() // GracefulStop waits forever for streaming RPCs; force after a deadline
	}
	return nil
}

// Healthcheck asks the gRPC health service at addr whether it is SERVING. Used by the
// -healthcheck flag (Docker) and by the demo to wait until the service is ready.
func Healthcheck(ctx context.Context, addr string) error {
	conn, err := grpc.NewClient(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	resp, err := healthpb.NewHealthClient(conn).Check(ctx, &healthpb.HealthCheckRequest{})
	if err != nil {
		return err
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		return fmt.Errorf("users-svc health status %s", resp.GetStatus())
	}
	return nil
}
