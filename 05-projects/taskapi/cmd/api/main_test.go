package main

import (
	"bytes"
	"context"
	"database/sql"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

// syncBuffer is a bytes.Buffer safe for the server goroutine to write while the test reads.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// freeAddr asks the OS for an unused port. (Tiny race window between closing and re-binding;
// acceptable in tests.)
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func testEnv(t *testing.T, addr string) func(string) string {
	env := map[string]string{
		"TASKAPI_JWT_SECRET": "0123456789abcdef0123456789abcdef",
		"TASKAPI_ADDR":       addr,
		"TASKAPI_DB_PATH":    filepath.Join(t.TempDir(), "main.db"),
	}
	return func(k string) string { return env[k] }
}

// The whole application, in-process: start, serve, then stop on context cancellation exactly as
// a SIGINT/SIGTERM would, and verify the shutdown is clean.
func TestRunServesAndShutsDownGracefully(t *testing.T) {
	addr := freeAddr(t)
	var out syncBuffer

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, nil, testEnv(t, addr), &out) }()

	// Wait until the server answers. Startup includes one Argon2id hash at production cost, which
	// is slow under the race detector, hence the generous deadline.
	var status int
	for range 1000 {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/readyz", nil)
		res, err := http.DefaultClient.Do(req)
		if err == nil {
			res.Body.Close()
			status = res.StatusCode
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if status != http.StatusOK {
		t.Fatalf("readyz status = %d\nlogs:\n%s", status, out.String())
	}

	cancel() // what SIGINT does
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run returned %v\nlogs:\n%s", err, out.String())
		}
	case <-time.After(10 * time.Second):
		t.Fatal("run did not return after cancellation")
	}

	logs := out.String()
	for _, want := range []string{`"msg":"listening"`, `"msg":"shutdown signal received, draining"`, `"msg":"shutdown complete"`} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %s:\n%s", want, logs)
		}
	}
	if strings.Contains(logs, "0123456789abcdef") {
		t.Errorf("JWT secret leaked into logs:\n%s", logs)
	}
	if !strings.Contains(logs, "[REDACTED]") {
		t.Errorf("expected redacted secret in startup config log:\n%s", logs)
	}
}

func TestRunRejectsBadConfig(t *testing.T) {
	var out syncBuffer
	err := run(t.Context(), nil, func(string) string { return "" }, &out)
	if err == nil || !strings.Contains(err.Error(), "TASKAPI_JWT_SECRET") {
		t.Fatalf("got %v, want an error naming TASKAPI_JWT_SECRET", err)
	}
}

func TestRunRejectsBusyPort(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	var out syncBuffer
	err = run(t.Context(), nil, testEnv(t, ln.Addr().String()), &out)
	if err == nil || !strings.Contains(err.Error(), "listen") {
		t.Fatalf("got %v, want a listen error", err)
	}
}

func TestRunMigrateOnly(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "migrate.db")
	getenv := func(k string) string {
		return map[string]string{
			"TASKAPI_JWT_SECRET": "0123456789abcdef0123456789abcdef",
			"TASKAPI_DB_PATH":    dbPath,
		}[k]
	}
	var out syncBuffer
	if err := run(t.Context(), []string{"-migrate-only"}, getenv, &out); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}

	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRowContext(t.Context(), `SELECT count(*) FROM sqlite_master WHERE name IN ('users','projects','tasks')`).Scan(&n); err != nil || n != 3 {
		t.Fatalf("tables created = %d, err = %v", n, err)
	}
}
