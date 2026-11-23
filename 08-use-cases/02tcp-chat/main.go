// Use case 02: a raw TCP chat server (net package, no HTTP, no frameworks).
//
// Go's net package gives you the same primitives as C sockets (Listen, Accept, Dial, Read,
// Write) but goroutines make them pleasant: the classic design is ONE GOROUTINE PER
// CONNECTION. Blocking reads are fine because blocking a goroutine is cheap (a few KB), so
// you write straight-line code instead of callback state machines. Go's runtime multiplexes
// those blocked goroutines onto epoll/kqueue for you.
//
// Architecture used here:
//
//		conn --> reader goroutine --events--> HUB goroutine --out chan--> writer goroutine --> conn
//
//	  - Reader goroutine (one per client): parses lines, sends events (join, say, nick, leave) to the hub.
//	  - Writer goroutine (one per client): drains the client's `out` channel to the socket.
//	  - Hub goroutine (exactly one): owns the set of clients and the nickname table.
//
// WHY A HUB AVOIDS LOCKS: all shared state (who is connected, who owns which nickname) is
// touched by exactly one goroutine, so there is nothing to race on and no mutex to forget.
// Others talk to it through a channel ("share memory by communicating"). It also gives one
// natural place to enforce policy: the hub uses a NON-BLOCKING send to each client's
// buffered `out` channel, so one stuck client can never stall everyone else. A client whose
// buffer is full is dropped (slow-consumer policy).
//
// Framing with bufio: TCP is a byte STREAM, not a message protocol. One Write on the sender
// may arrive as several Reads, and several Writes may arrive glued together. You must define
// message boundaries yourself. Here a message is one line ending in "\n", and
// bufio.Scanner (default split: lines) re-assembles them. Always cap the line length with
// Scanner.Buffer, otherwise a client that never sends "\n" makes you buffer forever.
// Binary protocols use a length prefix instead (4-byte length + payload), see encoding/binary.
//
// Timeouts: net.Conn has no default deadline. A silent client would hold a goroutine and a
// file descriptor forever. SetReadDeadline before every read cycle (idle timeout) and
// SetWriteDeadline before writes (a client that stops reading must not block our writer).
//
// Graceful close: cancelling the context makes the hub tell everyone, close every client's
// `out` channel (the writer flushes what is queued, then closes the socket), and Serve waits
// for all goroutines to finish via a WaitGroup. Rule of thumb: the goroutine that SENDS on a
// channel is the one that closes it; here only the hub (or the reader before registration).
//
// Gotchas:
//   - Closing a net.Conn from another goroutine is the way to unblock a pending Read.
//   - Check net.Error.Timeout() / errors.Is(err, os.ErrDeadlineExceeded) to tell timeouts from real errors.
//   - Many small Write calls = many syscalls. Wrap in a bufio.Writer if you write a lot.
//
// Try it:
//
//	go run ./08-use-cases/02tcp-chat                 # demo with three in-process clients
//	go run ./08-use-cases/02tcp-chat -serve :9000    # real server, then in other terminals:
//	nc localhost 9000                                # type a nickname, then chat. /who /nick x /quit
//
// In industry: raw TCP is what you write for custom protocols: Redis-compatible servers,
// proxies and load balancers (Caddy, Traefik, HAProxy-style tools), game servers, database
// wire protocols (CockroachDB, Vitess speak MySQL/Postgres wire formats in Go) and
// message brokers (NATS is Go). Discord famously moved services from Python/Go to
// Go/Rust for the huge number of long-lived connections. For browser clients you would speak
// WebSockets (next lesson) instead of raw TCP.
package main

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"time"
)

const (
	maxLine       = 1024             // longest accepted line in bytes
	outBuffer     = 16               // messages queued per client before we call it a slow consumer
	nickTimeout   = 10 * time.Second // time to send a nickname after connecting
	writeDeadline = 5 * time.Second
)

// client is the hub-visible state of one connection. nick is owned by the hub goroutine.
type client struct {
	conn net.Conn
	out  chan string // hub -> writer. Only the hub (or handle before joining) sends/closes.
	nick string      // touched ONLY by the hub goroutine, so no lock
}

type eventKind int

const (
	evJoin eventKind = iota
	evLeave
	evSay
	evNick
	evWho
)

// event is the single message type the hub receives. One channel keeps ordering simple.
type event struct {
	kind  eventKind
	c     *client
	text  string       // nick, chat text or farewell, depending on kind
	reply chan<- error // evJoin: nick accepted or rejected
}

// Server is the chat server. Zero global state: everything hangs off this struct.
type Server struct {
	ln     net.Listener
	idle   time.Duration
	log    *slog.Logger
	events chan event
	done   chan struct{} // closed when the hub exits
	wg     sync.WaitGroup
}

func NewServer(ln net.Listener, idle time.Duration, log *slog.Logger) *Server {
	return &Server{ln: ln, idle: idle, log: log, events: make(chan event), done: make(chan struct{})}
}

// Serve runs until ctx is cancelled, then shuts down gracefully and returns once every
// goroutine it started has exited.
func (s *Server) Serve(ctx context.Context) error {
	s.wg.Go(func() { s.hub(ctx) })

	// Unblock Accept when shutting down: closing the listener makes Accept return an error.
	stop := context.AfterFunc(ctx, func() { s.ln.Close() })
	defer stop()

	for {
		conn, err := s.ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				s.wg.Wait() // graceful: wait for hub, readers and writers
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue // transient; Accept can be retried
			}
			return err
		}
		s.wg.Go(func() { s.handle(ctx, conn) })
	}
}

// send delivers an event to the hub, giving up if the hub has already exited.
func (s *Server) send(e event) bool {
	select {
	case s.events <- e:
		return true
	case <-s.done:
		return false
	}
}

// hub is the ONLY goroutine that touches the client set and nickname table.
func (s *Server) hub(ctx context.Context) {
	defer close(s.done)
	clients := make(map[*client]struct{})
	nicks := make(map[string]*client)

	// deliver does a non-blocking send. If the client's buffer is full it is a slow consumer
	// and gets evicted instead of blocking the hub (and therefore everyone).
	var drop func(c *client, farewell string)
	deliver := func(c *client, msg string) {
		select {
		case c.out <- msg:
		default:
			s.log.Warn("slow consumer dropped", "nick", c.nick)
			drop(c, "")
		}
	}
	broadcast := func(except *client, msg string) {
		// Collect first: drop() mutates the map and must not run while ranging over it.
		var targets []*client
		for c := range clients {
			if c != except {
				targets = append(targets, c)
			}
		}
		for _, c := range targets {
			deliver(c, msg)
		}
	}
	drop = func(c *client, farewell string) {
		if _, ok := clients[c]; !ok {
			return // already removed (reader and eviction can race to here)
		}
		delete(clients, c)
		delete(nicks, c.nick)
		if farewell != "" {
			select { // best effort: never block on a leaving client
			case c.out <- farewell:
			default:
			}
		}
		close(c.out) // writer flushes the queue, then closes the socket
		broadcast(nil, fmt.Sprintf("* %s left", c.nick))
	}

	for {
		select {
		case <-ctx.Done():
			for c := range clients {
				select {
				case c.out <- "* server shutting down":
				default:
				}
				close(c.out)
			}
			return

		case e := <-s.events:
			switch e.kind {
			case evJoin:
				if err := validNick(e.text); err != nil {
					e.reply <- err
					continue
				}
				if _, taken := nicks[e.text]; taken {
					e.reply <- fmt.Errorf("nickname %q is taken", e.text)
					continue
				}
				e.c.nick = e.text
				clients[e.c] = struct{}{}
				nicks[e.text] = e.c
				e.reply <- nil
				deliver(e.c, fmt.Sprintf("* welcome %s, %d online. commands: /who /nick <name> /quit", e.text, len(clients)))
				broadcast(e.c, fmt.Sprintf("* %s joined", e.text))
				s.log.Info("joined", "nick", e.text, "online", len(clients))

			case evLeave:
				drop(e.c, e.text)

			case evSay:
				if _, ok := clients[e.c]; ok {
					broadcast(e.c, fmt.Sprintf("%s: %s", e.c.nick, e.text))
				}

			case evNick:
				if _, ok := clients[e.c]; !ok {
					continue
				}
				if err := validNick(e.text); err != nil {
					deliver(e.c, "! "+err.Error())
				} else if _, taken := nicks[e.text]; taken {
					deliver(e.c, fmt.Sprintf("! nickname %q is taken", e.text))
				} else {
					old := e.c.nick
					delete(nicks, old)
					e.c.nick = e.text
					nicks[e.text] = e.c
					broadcast(nil, fmt.Sprintf("* %s is now %s", old, e.text))
				}

			case evWho:
				names := make([]string, 0, len(nicks))
				for n := range nicks {
					names = append(names, n)
				}
				slices.Sort(names) // map order is random; sort for stable output
				deliver(e.c, "* online: "+strings.Join(names, ", "))
			}
		}
	}
}

func validNick(n string) error {
	if n == "" || len(n) > 16 {
		return errors.New("nickname must be 1-16 characters")
	}
	for _, r := range n {
		ok := r == '_' || r == '-' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
		if !ok {
			return errors.New("nickname may only contain letters, digits, _ and -")
		}
	}
	return nil
}

// handle is the per-connection READER goroutine.
func (s *Server) handle(ctx context.Context, conn net.Conn) {
	c := &client{conn: conn, out: make(chan string, outBuffer)}
	s.wg.Go(func() { writeLoop(c) })

	// On shutdown, force any blocked Read to return by expiring its deadline.
	stop := context.AfterFunc(ctx, func() { conn.SetReadDeadline(time.Now()) })
	defer stop()

	sc := bufio.NewScanner(conn)
	sc.Buffer(make([]byte, 0, maxLine), maxLine) // lines longer than maxLine -> bufio.ErrTooLong

	// Phase 1: handshake. Before joining, only this goroutine may touch c.out.
	c.out <- "nickname?"
	conn.SetReadDeadline(time.Now().Add(nickTimeout))
	if !sc.Scan() {
		close(c.out)
		return
	}
	reply := make(chan error, 1) // buffered so the hub never blocks answering us
	if !s.send(event{kind: evJoin, c: c, text: strings.TrimSpace(sc.Text()), reply: reply}) {
		close(c.out)
		return
	}
	select {
	case err := <-reply:
		if err != nil {
			c.out <- "! " + err.Error()
			close(c.out)
			return
		}
	case <-s.done:
		close(c.out)
		return
	}

	// Phase 2: registered. From now on only the hub touches c.out.
	farewell := "bye"
	for {
		conn.SetReadDeadline(time.Now().Add(s.idle)) // fresh idle window per message
		if !sc.Scan() {
			switch err := sc.Err(); {
			case ctx.Err() != nil:
				return // shutdown: the hub already told and closed us
			case errors.Is(err, os.ErrDeadlineExceeded):
				farewell = "* idle timeout, disconnecting"
			case errors.Is(err, bufio.ErrTooLong):
				farewell = "* line too long, disconnecting"
			default:
				farewell = "" // EOF or network error: nobody left to tell
			}
			break
		}
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
		case line == "/quit":
			s.send(event{kind: evLeave, c: c, text: "bye"})
			return
		case line == "/who":
			s.send(event{kind: evWho, c: c})
		case strings.HasPrefix(line, "/nick "):
			s.send(event{kind: evNick, c: c, text: strings.TrimSpace(strings.TrimPrefix(line, "/nick "))})
		case strings.HasPrefix(line, "/"):
			s.send(event{kind: evSay, c: c, text: line}) // unknown command: treat as text
		default:
			s.send(event{kind: evSay, c: c, text: line})
		}
	}
	s.send(event{kind: evLeave, c: c, text: farewell})
}

// writeLoop is the per-connection WRITER goroutine. It exits when the hub closes c.out, after
// flushing whatever was already queued, and then closes the socket. That close is also what
// finally unblocks the reader goroutine if it is still in Read.
func writeLoop(c *client) {
	defer c.conn.Close()
	for msg := range c.out {
		c.conn.SetWriteDeadline(time.Now().Add(writeDeadline))
		if _, err := fmt.Fprintf(c.conn, "%s\n", msg); err != nil {
			return // peer gone or too slow; the reader will notice and leave
		}
	}
}

// ---------------------------------------------------------------------------------------
// Demo: three in-process clients.

// demoClient is a tiny line-based TCP client for the demo.
type demoClient struct {
	name  string
	conn  net.Conn
	lines chan string
}

func dialDemo(addr, name string) (*demoClient, error) {
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		return nil, err
	}
	d := &demoClient{name: name, conn: conn, lines: make(chan string, 64)}
	go func() { // reader: turn the byte stream back into lines
		defer close(d.lines)
		sc := bufio.NewScanner(conn)
		for sc.Scan() {
			d.lines <- sc.Text()
		}
	}()
	return d, nil
}

func (d *demoClient) say(text string) {
	fmt.Printf("%-6s > %s\n", d.name, text)
	fmt.Fprintf(d.conn, "%s\n", text)
}

// expect reads (and prints) lines until one contains want. Waiting for a specific line, not
// sleeping, is what keeps this demo deterministic.
func (d *demoClient) expect(want string) {
	timeout := time.After(3 * time.Second)
	for {
		select {
		case line, ok := <-d.lines:
			if !ok {
				fmt.Printf("%-6s   (connection closed)\n", d.name)
				return
			}
			fmt.Printf("%-6s < %s\n", d.name, line)
			if strings.Contains(line, want) {
				return
			}
		case <-timeout:
			fmt.Printf("%-6s   (timed out waiting for %q)\n", d.name, want)
			return
		}
	}
}

func demoLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{} // drop timestamps so demo output is stable
			}
			return a
		},
	}))
}

func runDemo() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0") // port 0: the OS picks a free port
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	fmt.Println("server listening on", addr)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // also called explicitly below to demo shutdown; cancel is idempotent
	srv := NewServer(ln, 2*time.Minute, demoLogger())
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	// Everyone joins in turn. We wait for each welcome/join line before moving on.
	alice, err := dialDemo(addr, "alice")
	if err != nil {
		return err
	}
	alice.expect("nickname?")
	alice.say("alice")
	alice.expect("welcome alice")

	bob, _ := dialDemo(addr, "bob")
	bob.expect("nickname?")
	bob.say("bob")
	bob.expect("welcome bob")
	alice.expect("bob joined")

	carol, _ := dialDemo(addr, "carol")
	carol.expect("nickname?")
	carol.say("bob") // duplicate nickname: rejected, connection closed
	carol.expect("taken")
	carol, _ = dialDemo(addr, "carol")
	carol.expect("nickname?")
	carol.say("carol")
	carol.expect("welcome carol")
	alice.expect("carol joined")
	bob.expect("carol joined")

	fmt.Println("\n-- chatting --")
	alice.say("hello everyone")
	bob.expect("alice: hello everyone")
	carol.expect("alice: hello everyone")
	bob.say("hi alice! try /who")
	alice.expect("bob: hi alice")
	carol.expect("bob: hi alice")
	carol.say("/who")
	carol.expect("online:")
	bob.say("/nick bobby")
	alice.expect("bob is now bobby")
	carol.say("/quit")
	carol.expect("bye")
	alice.expect("carol left")

	fmt.Println("\n-- graceful shutdown: server tells remaining clients and closes --")
	cancel()
	alice.expect("shutting down")
	bobDone := make(chan struct{})
	go func() {
		defer close(bobDone)
		for range bob.lines {
		}
	}() // drain until EOF
	<-bobDone
	if err := <-serveDone; err != nil {
		return err
	}
	fmt.Println("server stopped cleanly (all goroutines joined)")

	return runIdleDemo()
}

// runIdleDemo shows read deadlines: a server with a 150ms idle timeout disconnects a silent client.
func runIdleDemo() error {
	fmt.Println("\n-- idle timeout (server started with 150ms idle limit) --")
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	srv := NewServer(ln, 150*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	serveDone := make(chan error, 1)
	go func() { serveDone <- srv.Serve(ctx) }()

	sleepy, err := dialDemo(ln.Addr().String(), "sleepy")
	if err != nil {
		return err
	}
	sleepy.expect("nickname?")
	sleepy.say("sleepy")
	sleepy.expect("welcome")
	fmt.Println("sleepy   (says nothing...)")
	sleepy.expect("idle timeout")
	cancel()
	return <-serveDone
}

func main() {
	serve := flag.String("serve", "", "listen on this address (e.g. :9000) instead of running the demo")
	idle := flag.Duration("idle", 5*time.Minute, "disconnect clients silent for this long (with -serve)")
	flag.Parse()

	if *serve == "" {
		if err := runDemo(); err != nil {
			fmt.Fprintln(os.Stderr, "demo failed:", err)
			os.Exit(1)
		}
		return
	}

	ln, err := net.Listen("tcp", *serve)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	// SIGINT/SIGTERM cancel the context, which triggers the graceful shutdown path above.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Printf("chat server on %s. Try: nc localhost %s\n", ln.Addr(), portOf(ln.Addr()))
	if err := NewServer(ln, *idle, slog.Default()).Serve(ctx); err != nil {
		fmt.Fprintln(os.Stderr, "serve:", err)
		os.Exit(1)
	}
}

func portOf(a net.Addr) string {
	_, port, err := net.SplitHostPort(a.String())
	if err != nil {
		return "9000"
	}
	return port
}
