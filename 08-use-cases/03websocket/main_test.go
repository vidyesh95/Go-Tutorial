package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func quietServer(t *testing.T, origins []string) (*Server, *httptest.Server, context.CancelFunc) {
	t.Helper()
	srv := NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), origins, time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	hubDone := make(chan struct{})
	go func() { defer close(hubDone); srv.hub.run(ctx) }()
	ts := httptest.NewServer(srv.Handler())
	stopHub := func() { cancel(); <-hubDone }
	t.Cleanup(func() {
		stopHub() // close all websockets via the hub first: ts.Close alone would block on them
		ts.Close()
	})
	return srv, ts, stopHub
}

func dial(t *testing.T, ts *httptest.Server, nick string, h http.Header) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	c, resp, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws?nick="+nick, &websocket.DialOptions{HTTPHeader: h})
	if err == nil {
		t.Cleanup(func() { c.CloseNow() })
	}
	return c, resp, err
}

func readMsg(t *testing.T, c *websocket.Conn) chatMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	var m chatMsg
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestChatRoundTrip(t *testing.T) {
	_, ts, _ := quietServer(t, nil)
	alice, _, err := dial(t, ts, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	if m := readMsg(t, alice); m.Type != "sys" || !strings.Contains(m.Text, "welcome alice") {
		t.Fatalf("unexpected first message %+v", m)
	}
	bob, _, err := dial(t, ts, "bob", nil)
	if err != nil {
		t.Fatal(err)
	}
	readMsg(t, bob) // welcome
	if m := readMsg(t, alice); m.Text != "bob joined" {
		t.Fatalf("alice should be told bob joined, got %+v", m)
	}

	ctx := t.Context()
	if err := bob.Write(ctx, websocket.MessageText, []byte(`{"text":"  hi there  "}`)); err != nil {
		t.Fatal(err)
	}
	want := chatMsg{Type: "msg", From: "bob", Text: "hi there"} // trimmed by the server
	if got := readMsg(t, alice); got != want {
		t.Errorf("alice got %+v, want %+v", got, want)
	}
	if got := readMsg(t, bob); got != want {
		t.Errorf("sender should receive its own message, got %+v", got)
	}
}

// Origin checking is a security feature, so it deserves a test: a cross-site page must be refused.
func TestOriginCheck(t *testing.T) {
	_, ts, _ := quietServer(t, []string{"chat.example.com"})

	_, resp, err := dial(t, ts, "mallory", http.Header{"Origin": {"https://evil.example"}})
	if err == nil {
		t.Fatal("expected the cross-origin dial to fail")
	}
	if resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Errorf("response = %v, want 403", resp)
	}

	if _, _, err := dial(t, ts, "ok", http.Header{"Origin": {"https://chat.example.com"}}); err != nil {
		t.Errorf("allow-listed origin should connect: %v", err)
	}
}

// The hub can be tested with NO network at all, because it only talks to channels.
func TestSlowConsumerIsEvicted(t *testing.T) {
	h := newHub()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go h.run(ctx)

	slow := &client{nick: "slow", send: make(chan []byte, 1)} // never drained
	fast := &client{nick: "fast", send: make(chan []byte, 64)}
	h.join(slow) // welcome fills slow's 1-slot buffer
	h.join(fast)
	h.say(chatMsg{Type: "msg", From: "x", Text: "one"}) // slow's buffer is full -> evicted

	deadline := time.After(2 * time.Second)
	for h.dropped.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("slow client was never evicted")
		case <-time.After(5 * time.Millisecond):
		}
	}
	// Eviction closes the channel. Drain it: it must end (closed), and the reason is recorded.
	for range slow.send {
	}
	if slow.reason != reasonSlow {
		t.Errorf("reason = %q, want %q", slow.reason, reasonSlow)
	}
	if len(fast.send) == 0 {
		t.Error("fast client should still have received messages")
	}
}

// http.Server.Shutdown does not close WebSockets, so our own shutdown path must. A client
// should see the proper close status 1001 (going away), which tells it to reconnect elsewhere.
func TestShutdownSendsGoingAway(t *testing.T) {
	_, ts, stopHub := quietServer(t, nil)
	c, _, err := dial(t, ts, "alice", nil)
	if err != nil {
		t.Fatal(err)
	}
	readMsg(t, c) // welcome
	stopHub()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	_, _, err = c.Read(ctx)
	if got := websocket.CloseStatus(err); got != websocket.StatusGoingAway {
		t.Fatalf("close status = %v (err=%v), want StatusGoingAway", got, err)
	}
}
