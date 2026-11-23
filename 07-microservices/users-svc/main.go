// Command users-svc runs the users gRPC service.
//
//	ADDR     listen address (default :9090)
//	DB_PATH  SQLite file (default users.db)
//
//	go run ./07-microservices/users-svc
//	grpcurl -plaintext localhost:9090 list
//
// "-healthcheck" probes the running service and exits 0/1; Docker uses it because the
// distroless runtime image has no shell or grpc_health_probe.
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"slices"

	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/svc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/users-svc/userssvc"
)

func main() {
	addr := svc.Getenv("ADDR", ":9090")
	if slices.Contains(os.Args[1:], "-healthcheck") {
		if err := userssvc.Healthcheck(context.Background(), svc.DialAddr(addr)); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	svc.Main("users-svc", func(ctx context.Context, log *slog.Logger) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		tp, closeTracing := telemetry.FromEnv("users-svc")
		defer closeTracing()
		return userssvc.Run(ctx, ln, userssvc.Config{DBPath: svc.Getenv("DB_PATH", "users.db")}, log, tp)
	})
}
