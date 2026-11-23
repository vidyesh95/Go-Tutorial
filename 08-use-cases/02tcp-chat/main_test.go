package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"testing"
	"time"
)

// startServer runs a server on a random port and stops it (and waits for every goroutine)
// when the test ends. The wait is the point: -race plus a clean Serve return proves no leak.
func startServer(t *testing.T, idle time.Duration) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	srv := NewServer(ln, idle, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Serve returned %v", err)
			}
		case <-time.After(3 * time.Second):
			t.Error("Serve did not shut down in time (goroutine leak?)")
		}
	})
	return ln.Addr().String()
}

type testClient struct {
	t  *testing.T
	c  net.Conn
	sc *bufio.Scanner
}

func connect(t *testing.T, addr, nick string) *testClient {
	t.Helper()
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	tc := &testClient{t: t, c: c, sc: bufio.NewScanner(c)}
	tc.want("nickname?")
	tc.send(nick)
	return tc
}

func (tc *testClient) send(s string) {
	tc.t.Helper()
	if _, err := fmt.Fprintf(tc.c, "%s\n", s); err != nil {
		tc.t.Fatal(err)
	}
}

// next reads one line with a deadline so a bug fails the test instead of hanging it.
func (tc *testClient) next() (string, bool) {
	tc.c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if !tc.sc.Scan() {
		return "", false
	}
	return tc.sc.Text(), true
}

func (tc *testClient) want(line string) {
	tc.t.Helper()
	got, ok := tc.next()
	if !ok || got != line {
		tc.t.Fatalf("got %q (ok=%v), want %q", got, ok, line)
	}
}

func TestBroadcastAndNicknames(t *testing.T) {
	addr := startServer(t, time.Minute)
	alice := connect(t, addr, "alice")
	if line, _ := alice.next(); line != "* welcome alice, 1 online. commands: /who /nick <name> /quit" {
		t.Fatalf("unexpected welcome %q", line)
	}
	bob := connect(t, addr, "bob")
	bob.next() // welcome
	alice.want("* bob joined")

	bob.send("hello")
	alice.want("bob: hello") // the sender does not get an echo

	dup := connect(t, addr, "alice") // taken
	dup.want(`! nickname "alice" is taken`)
	if _, ok := dup.next(); ok {
		t.Error("server should close the connection after rejecting the nickname")
	}

	bob.send("/quit")
	alice.want("* bob left")
}

// TCP is a byte stream: the server must reassemble a line sent in two separate writes.
func TestLineFramingAcrossWrites(t *testing.T) {
	addr := startServer(t, time.Minute)
	alice := connect(t, addr, "alice")
	alice.next()
	bob := connect(t, addr, "bob")
	bob.next()
	alice.want("* bob joined")

	fmt.Fprint(bob.c, "split ")
	time.Sleep(20 * time.Millisecond) // make it likely the first half is read on its own
	fmt.Fprint(bob.c, "message\nsecond\n")
	alice.want("bob: split message")
	alice.want("bob: second")
}

func TestIdleTimeout(t *testing.T) {
	addr := startServer(t, 100*time.Millisecond)
	c := connect(t, addr, "sleepy")
	c.next() // welcome
	c.want("* idle timeout, disconnecting")
}

func TestShutdownClosesClients(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	srv := NewServer(ln, time.Minute, slog.New(slog.NewTextHandler(io.Discard, nil)))
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx) }()

	c := connect(t, ln.Addr().String(), "alice")
	c.next() // welcome
	cancel()
	c.want("* server shutting down")
	if _, ok := c.next(); ok {
		t.Error("connection should be closed after the shutdown notice")
	}
	if err := <-done; err != nil {
		t.Errorf("Serve: %v", err)
	}
}
