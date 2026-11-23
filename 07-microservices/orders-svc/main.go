// Command orders-svc runs the orders HTTP service.
//
//	ADDR        listen address (default :8081)
//	DB_PATH     SQLite file (default orders.db)
//	USERS_ADDR  users-svc gRPC address (default localhost:9090)
//
//	go run ./07-microservices/orders-svc
//
// "-healthcheck" probes the running service and exits 0/1 (used by Docker, see Dockerfile).
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
	"github.com/vidyesh95/Go-Tutorial/07-microservices/orders-svc/orderssvc"
)

func main() {
	addr := svc.Getenv("ADDR", ":8081")
	if slices.Contains(os.Args[1:], "-healthcheck") {
		if err := svc.HTTPHealthcheck(context.Background(), "http://"+svc.DialAddr(addr)+"/healthz"); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	svc.Main("orders-svc", func(ctx context.Context, log *slog.Logger) error {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		tp, closeTracing := telemetry.FromEnv("orders-svc")
		defer closeTracing()
		return orderssvc.Run(ctx, ln, orderssvc.Config{
			DBPath:    svc.Getenv("DB_PATH", "orders.db"),
			UsersAddr: svc.Getenv("USERS_ADDR", "localhost:9090"),
		}, log, tp)
	})
}
