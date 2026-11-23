// Command gateway runs the API gateway, the public entry point.
//
//	ADDR        listen address (default :8080)
//	ORDERS_URL  base URL of orders-svc (default http://localhost:8081)
//
//	go run ./07-microservices/gateway
//	curl localhost:8080/healthz
//
// "-healthcheck" probes the running gateway and exits 0/1 (used by Docker, see Dockerfile).
package main

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"slices"

	"github.com/vidyesh95/Go-Tutorial/07-microservices/gateway/gatewaysvc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/svc"
	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/telemetry"
)

func main() {
	addr := svc.Getenv("ADDR", ":8080")
	if slices.Contains(os.Args[1:], "-healthcheck") {
		if err := svc.HTTPHealthcheck(context.Background(), "http://"+svc.DialAddr(addr)+"/livez"); err != nil {
			fmt.Fprintln(os.Stderr, "unhealthy:", err)
			os.Exit(1)
		}
		return
	}
	svc.Main("gateway", func(ctx context.Context, log *slog.Logger) error {
		orders, err := url.Parse(svc.Getenv("ORDERS_URL", "http://localhost:8081"))
		if err != nil {
			return fmt.Errorf("ORDERS_URL: %w", err)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return err
		}
		tp, closeTracing := telemetry.FromEnv("gateway")
		defer closeTracing()
		cfg := gatewaysvc.Config{Routes: []gatewaysvc.Route{{Name: "orders", Target: orders}}}
		return gatewaysvc.Run(ctx, ln, cfg, log, tp)
	})
}
