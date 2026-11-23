// Package mailer sends the transactional emails of the auth flows (verify your email, reset your
// password). The service package declares the one-method interface it needs; this package holds
// implementations.
//
// Console writes the message to the application log, which is all you need to develop and demo
// the flows: copy the link from the log. In production, implement the same method on top of an
// email API or SMTP (SES, Postmark, Resend, ...), ideally behind a queue, so a slow provider
// does not slow down requests and failures can be retried.
package mailer

import (
	"context"
	"log/slog"
	"sync"
)

// Console logs every message instead of sending it. THIS DELIBERATELY LOGS THE LINK, WHICH
// CONTAINS A LIVE TOKEN: it is a development tool. Never use it where logs are shared or retained.
type Console struct {
	Log *slog.Logger
}

// Send implements the service.Mailer interface.
func (c Console) Send(ctx context.Context, to, subject, body string) error {
	c.Log.InfoContext(ctx, "email (console mailer, not sent)", "to", to, "subject", subject, "body", body)
	return nil
}

// Message is one captured email.
type Message struct {
	To, Subject, Body string
}

// Memory records messages instead of sending them. It is the test double: a test performs an
// action ("forgot password"), then reads the token out of the captured email, exactly like a
// user would click the link. Safe for concurrent use.
type Memory struct {
	mu   sync.Mutex
	sent []Message
	// Err, when set, is returned by Send (and nothing is recorded), to test mail failures.
	Err error
}

// Send implements the service.Mailer interface.
func (m *Memory) Send(_ context.Context, to, subject, body string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.Err != nil {
		return m.Err
	}
	m.sent = append(m.sent, Message{To: to, Subject: subject, Body: body})
	return nil
}

// Sent returns a copy of everything sent so far.
func (m *Memory) Sent() []Message {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]Message(nil), m.sent...)
}

// Last returns the most recent message and whether there is one.
func (m *Memory) Last() (Message, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.sent) == 0 {
		return Message{}, false
	}
	return m.sent[len(m.sent)-1], true
}
