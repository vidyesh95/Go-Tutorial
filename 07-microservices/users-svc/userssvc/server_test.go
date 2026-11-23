package userssvc

import (
	"context"
	"io"
	"log/slog"
	"net"
	"path/filepath"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthpb "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	usersv1 "github.com/vidyesh95/Go-Tutorial/07-microservices/gen/users/v1"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/sqlitedb"
)

// newClient starts the real gRPC server (real interceptors, real SQLite) on an IN-MEMORY
// listener (bufconn) and returns a connected client. No ports, no network, yet requests go
// through the full gRPC stack: serialization, status codes, interceptors. This is the standard
// way to test a gRPC service.
func newClient(t *testing.T) (usersv1.UsersServiceClient, *grpc.ClientConn) {
	t.Helper()
	db, err := sqlitedb.Open(t.Context(), filepath.Join(t.TempDir(), "users.db"), Schema)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	srv, _ := NewGRPCServer(NewStore(db), log, nil)
	lis := bufconn.Listen(1 << 20)
	go srv.Serve(lis)
	t.Cleanup(srv.Stop)

	conn, err := grpc.NewClient("passthrough:///bufnet",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) { return lis.DialContext(ctx) }),
		grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return usersv1.NewUsersServiceClient(conn), conn
}

func TestCreateAndGetUser(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()

	created, err := c.CreateUser(ctx, &usersv1.CreateUserRequest{Name: "Ada", Email: "ada@example.com"})
	if err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	u := created.GetUser()
	if u.GetId() == "" || u.GetName() != "Ada" || u.GetEmail() != "ada@example.com" || u.GetCreatedAt() == nil {
		t.Fatalf("unexpected user: %v", u)
	}

	got, err := c.GetUser(ctx, &usersv1.GetUserRequest{Id: u.GetId()})
	if err != nil {
		t.Fatalf("GetUser: %v", err)
	}
	if got.GetUser().GetEmail() != "ada@example.com" {
		t.Fatalf("GetUser returned %v", got.GetUser())
	}
}

func TestStatusCodes(t *testing.T) {
	c, _ := newClient(t)
	ctx := t.Context()
	if _, err := c.CreateUser(ctx, &usersv1.CreateUserRequest{Name: "Ada", Email: "ada@example.com"}); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		call func() error
		want codes.Code
	}{
		{"get unknown id", func() error {
			_, err := c.GetUser(ctx, &usersv1.GetUserRequest{Id: "nope"})
			return err
		}, codes.NotFound},
		{"get empty id", func() error {
			_, err := c.GetUser(ctx, &usersv1.GetUserRequest{})
			return err
		}, codes.InvalidArgument},
		{"create without name", func() error {
			_, err := c.CreateUser(ctx, &usersv1.CreateUserRequest{Email: "x@example.com"})
			return err
		}, codes.InvalidArgument},
		{"create with bad email", func() error {
			_, err := c.CreateUser(ctx, &usersv1.CreateUserRequest{Name: "X", Email: "not-an-email"})
			return err
		}, codes.InvalidArgument},
		{"create duplicate email", func() error {
			_, err := c.CreateUser(ctx, &usersv1.CreateUserRequest{Name: "Other", Email: "ada@example.com"})
			return err
		}, codes.AlreadyExists},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := status.Code(tt.call()); got != tt.want {
				t.Fatalf("code = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestHealthService(t *testing.T) {
	_, conn := newClient(t)
	resp, err := healthpb.NewHealthClient(conn).Check(t.Context(), &healthpb.HealthCheckRequest{Service: ServiceName})
	if err != nil {
		t.Fatal(err)
	}
	if resp.GetStatus() != healthpb.HealthCheckResponse_SERVING {
		t.Fatalf("status = %v", resp.GetStatus())
	}
}

func TestRecoveryInterceptorTurnsPanicIntoInternal(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	panicky := func(context.Context, any) (any, error) { panic("boom") }
	_, err := RecoveryInterceptor(log)(t.Context(), nil, &grpc.UnaryServerInfo{FullMethod: "/x/Y"}, panicky)
	if status.Code(err) != codes.Internal {
		t.Fatalf("code = %v, want Internal", status.Code(err))
	}
}
