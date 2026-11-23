// Use case 03: real-time chat over WebSockets (github.com/coder/websocket).
//
// HTTP is request/response: the server can only talk when asked. WebSocket starts as a normal
// HTTP request ("Upgrade: websocket"), then the same TCP connection becomes a full-duplex,
// message-oriented channel where either side can send at any time. That is what chat,
// live dashboards, multiplayer cursors and notifications need.
//
// Architecture (same hub idea as the TCP chat lesson, now with a browser UI):
//
//		browser/Dial <--ws--> handler goroutine (READS) --> hub goroutine --> client.send chan
//		                      writer goroutine  (WRITES) <--------------------------'
//
//	  - The hub goroutine owns the set of clients. No mutex: everyone talks to it over channels.
//	  - Each client has a BUFFERED send channel. The hub uses a non-blocking send; if the buffer
//	    is full the client is a slow consumer and is dropped. One slow phone on a bad network
//	    must never slow down a room of 10,000 people. (Alternative: drop the message, not the client.)
//	  - One reader + one writer per connection: the library allows concurrent Write calls but
//	    only ONE goroutine may call Read, and you MUST keep reading or pings/pongs/close frames
//	    are never processed.
//
// Production concerns covered here:
//   - Origin check: browsers let any web page open a WebSocket to your server, with the user's
//     cookies (cross-site WebSocket hijacking). websocket.Accept rejects requests whose Origin
//     host differs from the request Host unless you list it in AcceptOptions.OriginPatterns.
//     Never use InsecureSkipVerify in production.
//   - Keepalive: a server-side Ping every N seconds detects dead peers (laptop lid closed, NAT
//     dropped the mapping) and keeps idle connections alive through proxies/load balancers that
//     time out silent connections (nginx defaults to 60s).
//   - Limits: SetReadLimit caps message size (default 32KiB); we also cap text length.
//   - Timeouts on every write, so a stalled peer cannot pin a goroutine forever.
//   - Shutdown: http.Server.Shutdown does NOT close hijacked connections (WebSockets). You must
//     close them yourself. Here a cancelled context makes the hub close every client with the
//     status "going away" (1001) and the browser/client can reconnect elsewhere.
//   - Escape output in the browser (index.html uses textContent, never innerHTML) to avoid XSS.
//
// Embedded UI: `//go:embed index.html` bakes the HTML into the binary, so deploying is
// copying one file. embed.FS is read-only and served with http.FileServerFS.
//
// Scaling note: a hub in one process only reaches clients connected to THAT process. With
// several replicas, publish messages through Redis pub/sub, NATS or Kafka and have every
// replica fan out to its local clients. Sticky sessions or a connection gateway help too.
//
// Try it:
//
//	go run ./08-use-cases/03websocket                 # demo: in-process websocket.Dial clients
//	go run ./08-use-cases/03websocket -serve :8080    # open http://localhost:8080 in two tabs
//	go run ./08-use-cases/03websocket -serve :8080 -origin chat.example.com   # allow another site
//
// In industry: Discord, Slack and Figma-style products keep millions of long-lived connections
// open (Discord's gateway is famously Elixir/Rust/Go-heavy). In Go, coder/websocket (formerly
// nhooyr.io/websocket) is the modern, context-aware, maintained choice. gorilla/websocket was
// archived for a while and is still very common in older code. Also see Centrifugo (Go) and
// server-sent events (SSE) for one-way streams, which need no library at all.
package main

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
)

//go:embed index.html
var assets embed.FS

const (
	sendBuffer   = 16 // queued messages per client before it counts as a slow consumer
	maxTextRunes = 500
	writeTimeout = 5 * time.Second
	reasonSlow   = "too slow"
	reasonStop   = "server shutting down"
)

// chatMsg is the JSON sent to browsers. Type is "msg" (user text) or "sys" (server notice).
type chatMsg struct {
	Type string `json:"type"`
	From string `json:"from,omitempty"`
	Text string `json:"text"`
}

// client is one connected user as the hub sees it.
type client struct {
	nick   string
	send   chan []byte // hub -> writer goroutine. ONLY the hub sends and closes.
	reason string      // why the hub closed send; written before close(send), read after: no race
}

// hub owns all membership state. Its goroutine is the only one that touches `clients`.
type hub struct {
	joins  chan *client
	leaves chan *client
	msgs   chan chatMsg
	done   chan struct{} // closed when run returns

	dropped atomic.Int64 // slow consumers evicted (read by tests and the demo)
}

func newHub() *hub {
	return &hub{joins: make(chan *client), leaves: make(chan *client), msgs: make(chan chatMsg), done: make(chan struct{})}
}

func (h *hub) run(ctx context.Context) {
	defer close(h.done)
	clients := make(map[*client]struct{})

	evict := func(c *client, reason string) {
		if _, ok := clients[c]; !ok {
			return
		}
		delete(clients, c)
		c.reason = reason
		close(c.send)
	}
	// deliver never blocks: full buffer == slow consumer == evicted.
	deliver := func(c *client, m chatMsg) {
		b, _ := json.Marshal(m) // chatMsg only holds strings; Marshal cannot fail
		select {
		case c.send <- b:
		default:
			h.dropped.Add(1)
			evict(c, reasonSlow)
		}
	}
	broadcast := func(except *client, m chatMsg) {
		// Snapshot first: deliver may evict (mutate the map) and we must not range over a changing map.
		snapshot := make([]*client, 0, len(clients))
		for c := range clients {
			if c != except {
				snapshot = append(snapshot, c)
			}
		}
		for _, c := range snapshot {
			deliver(c, m)
		}
	}

	for {
		select {
		case <-ctx.Done():
			for c := range clients {
				evict(c, reasonStop)
			}
			return
		case c := <-h.joins:
			clients[c] = struct{}{}
			deliver(c, chatMsg{Type: "sys", Text: fmt.Sprintf("welcome %s, %d online", c.nick, len(clients))})
			broadcast(c, chatMsg{Type: "sys", Text: c.nick + " joined"})
		case c := <-h.leaves:
			if _, ok := clients[c]; ok {
				evict(c, "")
				broadcast(nil, chatMsg{Type: "sys", Text: c.nick + " left"})
			}
		case m := <-h.msgs:
			broadcast(nil, m)
		}
	}
}

// join / leave / say are the hub's public API. They give up if the hub has stopped.
func (h *hub) join(c *client) bool {
	select {
	case h.joins <- c:
		return true
	case <-h.done:
		return false
	}
}

func (h *hub) leave(c *client) {
	select {
	case h.leaves <- c:
	case <-h.done:
	}
}

func (h *hub) say(m chatMsg) {
	select {
	case h.msgs <- m:
	case <-h.done:
	}
}

// Server wires the hub to HTTP.
type Server struct {
	hub       *hub
	log       *slog.Logger
	origins   []string      // extra allowed Origin host patterns (the request's own host is always allowed)
	pingEvery time.Duration // keepalive interval
	pings     atomic.Int64  // successful ping/pong round trips
	nextGuest atomic.Int64
	wg        sync.WaitGroup // tracks connection handlers so shutdown can wait for them
}

func NewServer(log *slog.Logger, origins []string, pingEvery time.Duration) *Server {
	return &Server{hub: newHub(), log: log, origins: origins, pingEvery: pingEvery}
}

// Handler returns the HTTP routes: the embedded UI at / and the WebSocket endpoint at /ws.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	ui, _ := fs.Sub(assets, ".") // serve the embedded files from their root
	mux.Handle("GET /{$}", http.FileServerFS(ui))
	mux.HandleFunc("GET /ws", s.handleWS)
	return mux
}

func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	s.wg.Add(1)
	defer s.wg.Done()

	// Accept performs the upgrade AND the origin check. On failure it has already written
	// the HTTP error response (403 for a bad origin), so we just return.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: s.origins})
	if err != nil {
		s.log.Warn("websocket accept failed", "err", err, "origin", r.Header.Get("Origin"))
		return
	}
	conn.SetReadLimit(4096) // bigger frames close the connection with StatusMessageTooBig

	nick := cleanNick(r.URL.Query().Get("nick"))
	if nick == "" {
		nick = fmt.Sprintf("guest%d", s.nextGuest.Add(1))
	}
	c := &client{nick: nick, send: make(chan []byte, sendBuffer)}
	if !s.hub.join(c) {
		conn.Close(websocket.StatusGoingAway, reasonStop)
		return
	}

	// ctx ends when this handler returns (it cancels the writer) or the request context ends.
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	writerDone := make(chan struct{})
	go func() {
		defer close(writerDone)
		s.writeLoop(ctx, conn, c)
	}()

	// Reader loop (this goroutine). Read blocks until a message, error or ctx cancel. It also
	// processes ping/pong/close frames internally, which is why we must never stop calling it.
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			break // client closed, network error, message too big, or shutdown
		}
		var in struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(data, &in) != nil {
			continue // ignore malformed JSON rather than dropping the user
		}
		text := strings.TrimSpace(in.Text)
		if text == "" || len([]rune(text)) > maxTextRunes {
			continue
		}
		s.hub.say(chatMsg{Type: "msg", From: c.nick, Text: text})
	}
	s.hub.leave(c)
	cancel()
	<-writerDone
	conn.CloseNow() // idempotent; makes sure the socket is released
}

// writeLoop is the only goroutine that writes to this connection (plus pings).
func (s *Server) writeLoop(ctx context.Context, conn *websocket.Conn, c *client) {
	ping := time.NewTicker(s.pingEvery)
	defer ping.Stop()
	for {
		select {
		case msg, ok := <-c.send:
			if !ok {
				// The hub closed our channel: say why with a proper close frame.
				switch c.reason {
				case reasonSlow:
					conn.Close(websocket.StatusPolicyViolation, reasonSlow)
				case reasonStop:
					conn.Close(websocket.StatusGoingAway, reasonStop)
				default:
					conn.Close(websocket.StatusNormalClosure, "")
				}
				return
			}
			wctx, cancel := context.WithTimeout(ctx, writeTimeout)
			err := conn.Write(wctx, websocket.MessageText, msg)
			cancel()
			if err != nil {
				return // the library already closed the connection; the reader will see the error
			}
		case <-ping.C:
			// Ping waits for the pong, which the reader goroutine consumes. No pong in time
			// means the peer is dead (or just very slow): drop it.
			pctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			err := conn.Ping(pctx)
			cancel()
			if err != nil {
				conn.CloseNow()
				return
			}
			s.pings.Add(1)
		case <-ctx.Done():
			return
		}
	}
}

func cleanNick(n string) string {
	n = strings.TrimSpace(n)
	if r := []rune(n); len(r) > 20 {
		n = string(r[:20])
	}
	return n
}

// Run starts the hub and the HTTP server on ln, and blocks until ctx is cancelled and
// everything has shut down.
func (s *Server) Run(ctx context.Context, ln net.Listener) error {
	hubCtx, stopHub := context.WithCancel(context.Background())
	defer stopHub()
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); s.hub.run(hubCtx) }()

	srv := &http.Server{Handler: s.Handler(), ReadHeaderTimeout: 5 * time.Second}
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		return err
	case <-ctx.Done():
	}

	// Shutdown order matters:
	//  1. Stop the hub: every client is closed with "going away" (Shutdown cannot do this).
	//  2. Shutdown the HTTP server: stop accepting, wait for handlers.
	//  3. Wait for our own handler goroutines.
	stopHub()
	<-hubDone
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := srv.Shutdown(shutCtx)
	s.wg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		err = nil
	}
	return err
}

// ---------------------------------------------------------------------------------------
// Demo

type demoClient struct {
	name string
	conn *websocket.Conn
}

func dialDemo(ctx context.Context, base, name string, header http.Header) (*demoClient, *http.Response, error) {
	url := "ws" + strings.TrimPrefix(base, "http") + "/ws?nick=" + name
	conn, resp, err := websocket.Dial(ctx, url, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		return nil, resp, err
	}
	return &demoClient{name: name, conn: conn}, resp, nil
}

func (d *demoClient) say(ctx context.Context, text string) error {
	fmt.Printf("%-6s > %s\n", d.name, text)
	b, _ := json.Marshal(map[string]string{"text": text})
	return d.conn.Write(ctx, websocket.MessageText, b)
}

// expect reads messages (printing them) until one contains want. Reading, not sleeping, keeps
// the demo deterministic. A close from the server is returned as an error.
func (d *demoClient) expect(ctx context.Context, want string) error {
	for {
		_, data, err := d.conn.Read(ctx)
		if err != nil {
			return err
		}
		var m chatMsg
		if err := json.Unmarshal(data, &m); err != nil {
			return err
		}
		if m.Type == "sys" {
			fmt.Printf("%-6s < [server] %s\n", d.name, m.Text)
		} else {
			fmt.Printf("%-6s < %s: %s\n", d.name, m.From, m.Text)
		}
		if strings.Contains(m.Text, want) {
			return nil
		}
	}
}

func runDemo() error {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()
	fmt.Println("server on", base)

	srv := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), nil, 40*time.Millisecond)
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	runDone := make(chan error, 1)
	go func() { runDone <- srv.Run(ctx, ln) }()

	cctx, ccancel := context.WithTimeout(context.Background(), 20*time.Second) // clients get their own ctx; safety net so the demo never hangs
	defer ccancel()

	// 1. The embedded UI is served by the same server.
	resp, err := http.Get(base + "/")
	if err != nil {
		return err
	}
	page, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	fmt.Printf("GET / -> %d, %d bytes of embedded index.html\n\n", resp.StatusCode, len(page))

	// 2. Three clients join and chat.
	alice, _, err := dialDemo(cctx, base, "alice", nil)
	if err != nil {
		return err
	}
	if err := alice.expect(cctx, "welcome"); err != nil {
		return err
	}
	bob, _, err := dialDemo(cctx, base, "bob", nil)
	if err != nil {
		return err
	}
	bob.expect(cctx, "welcome")
	alice.expect(cctx, "bob joined")
	carol, _, err := dialDemo(cctx, base, "carol", nil)
	if err != nil {
		return err
	}
	carol.expect(cctx, "welcome")
	alice.expect(cctx, "carol joined")
	bob.expect(cctx, "carol joined")

	fmt.Println()
	alice.say(cctx, "hello from alice")
	for _, c := range []*demoClient{alice, bob, carol} { // the sender gets its own message back too
		c.expect(cctx, "hello from alice")
	}
	bob.say(cctx, "bob here")
	for _, c := range []*demoClient{alice, bob, carol} {
		c.expect(cctx, "bob here")
	}
	carol.conn.Close(websocket.StatusNormalClosure, "bye")
	alice.expect(cctx, "carol left")
	bob.expect(cctx, "carol left")

	// 3. Origin check: a page on another site must not be able to connect.
	fmt.Println("\n-- origin check --")
	_, resp, err = dialDemo(cctx, base, "mallory", http.Header{"Origin": {"https://evil.example"}})
	if err != nil && resp != nil {
		fmt.Printf("dial with Origin https://evil.example -> HTTP %d (rejected)\n", resp.StatusCode)
	} else {
		return fmt.Errorf("evil origin was not rejected (err=%v)", err)
	}

	// 4. Slow consumer: a client that never drains its buffer is dropped, others unaffected.
	fmt.Println("\n-- slow consumer --")
	slow := &client{nick: "slowpoke", send: make(chan []byte, 2)} // tiny buffer, nobody reads it
	srv.hub.join(slow)
	for i := range 4 {
		alice.say(cctx, fmt.Sprintf("message %d", i+1))
		alice.expect(cctx, fmt.Sprintf("message %d", i+1))
		bob.expect(cctx, fmt.Sprintf("message %d", i+1))
	}
	fmt.Printf("hub evicted %d slow consumer(s); reason=%q; alice and bob kept chatting\n", srv.hub.dropped.Load(), slow.reason)

	// 5. Keepalive. The server pings every 40ms here (30s is typical). A peer answers pings
	// automatically, but only WHILE it is reading, so we keep both clients reading in the
	// background. Browsers do this for you; with a Go client you must call Read.
	type ended struct {
		name string
		err  error
	}
	endedCh := make(chan ended, 2)
	for _, c := range []*demoClient{alice, bob} {
		go func() { endedCh <- ended{c.name, c.expect(cctx, "never matches")} }()
	}
	time.Sleep(250 * time.Millisecond)
	fmt.Printf("\nkeepalive: %d ping/pong round trips completed while the room was idle\n", srv.pings.Load())

	// 6. Graceful shutdown: clients receive close code 1001 "going away".
	fmt.Println("\n-- shutdown --")
	stop()
	var results []ended
	for range 2 {
		results = append(results, <-endedCh)
	}
	slices.SortFunc(results, func(a, b ended) int { return strings.Compare(a.name, b.name) })
	for _, r := range results {
		fmt.Printf("%-6s connection ended: status=%v reason=%q\n", r.name, websocket.CloseStatus(r.err), closeReason(r.err))
	}
	if err := <-runDone; err != nil {
		return err
	}
	fmt.Println("server stopped cleanly")
	return nil
}

func closeReason(err error) string {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return ce.Reason
	}
	return ""
}

func main() {
	serve := flag.String("serve", "", "listen address (e.g. :8080) to run the real chat server instead of the demo")
	origins := flag.String("origin", "", "comma-separated extra allowed Origin hosts, e.g. chat.example.com")
	ping := flag.Duration("ping", 30*time.Second, "keepalive ping interval")
	flag.Parse()

	if *serve == "" {
		if err := runDemo(); err != nil {
			fmt.Fprintln(os.Stderr, "demo failed:", err)
			os.Exit(1)
		}
		return
	}

	var patterns []string
	for p := range strings.SplitSeq(*origins, ",") {
		if p = strings.TrimSpace(p); p != "" {
			patterns = append(patterns, p)
		}
	}
	ln, err := net.Listen("tcp", *serve)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen:", err)
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	fmt.Printf("chat UI: http://localhost%s  (open it in two tabs; Ctrl-C to stop)\n", portSuffix(ln))
	if err := NewServer(slog.Default(), patterns, *ping).Run(ctx, ln); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

func portSuffix(ln net.Listener) string {
	_, port, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		return ""
	}
	return ":" + port
}
