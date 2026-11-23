package orderssvc

import (
	"context"
	"fmt"
	"log/slog"
	"time"
)

// Event is a message for other services ("an order was created").
type Event struct {
	ID      string // unique and stable across redeliveries: consumers de-duplicate on it
	Topic   string
	Payload []byte // JSON
}

// EventBus is where events go. It is an interface so the rest of the service does not care what
// is behind it.
//
// IN PRODUCTION this is a Kafka / NATS JetStream / RabbitMQ / SNS+SQS / Google Pub/Sub producer:
// Publish would be `producer.Produce(topic, key, payload)` and would return only once the broker
// has ACKed the write. Other services (shipping, email, analytics) consume the topic at their
// own pace, and orders-svc neither knows nor cares whether they are up. That decoupling is the
// point of ASYNCHRONOUS communication: contrast with the synchronous gRPC call to users-svc,
// where orders-svc cannot proceed unless users-svc answers.
type EventBus interface {
	Publish(ctx context.Context, e Event) error
}

// LogBus is the tutorial's "message broker": it just logs the event.
type LogBus struct{ Log *slog.Logger }

func (b LogBus) Publish(_ context.Context, e Event) error {
	b.Log.Info("event published", "event_id", e.ID, "topic", e.Topic, "payload", string(e.Payload))
	return nil
}

// Relay moves events from the outbox table to the bus. It is the second half of the outbox
// pattern (the first half is the transactional write in Store.CreateOrder).
//
// AT-LEAST-ONCE DELIVERY: the relay publishes first and marks the row sent afterwards. If the
// process dies between the two, the row is still unsent and is published AGAIN on restart. So
// consumers may see the same event twice and must be idempotent (e.g. remember processed
// event IDs). "Exactly-once" delivery across a network does not exist; "at-least-once plus
// idempotent consumers" is how real systems achieve exactly-once EFFECTS.
//
// Events are published in outbox order, and a failure stops the batch so a later event never
// overtakes an earlier one.
//
// In industry: instead of polling, teams often tail the database's write-ahead log with
// Debezium (change data capture) so events appear within milliseconds without extra queries.
type Relay struct {
	Store     *Store
	Bus       EventBus
	Log       *slog.Logger
	Interval  time.Duration // pause between polls
	BatchSize int
}

// Run polls until ctx is cancelled, then returns. Start it in its own goroutine and wait for it
// during shutdown, as Run in run.go does.
func (r *Relay) Run(ctx context.Context) {
	ticker := time.NewTicker(r.Interval)
	defer ticker.Stop()
	for {
		if n, err := r.Poll(ctx); err != nil && ctx.Err() == nil {
			// Not fatal: the events stay in the outbox and the next tick retries them.
			r.Log.Warn("outbox relay: poll failed, will retry", "published", n, "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Poll publishes one batch of pending events and returns how many it published.
func (r *Relay) Poll(ctx context.Context) (int, error) {
	batch := r.BatchSize
	if batch <= 0 {
		batch = 100
	}
	events, err := r.Store.pending(ctx, batch)
	if err != nil {
		return 0, err
	}
	for i, p := range events {
		if err := r.Bus.Publish(ctx, p.event); err != nil {
			return i, fmt.Errorf("publish %s: %w", p.event.ID, err)
		}
		if err := r.Store.markSent(ctx, p.seq); err != nil {
			return i, err // published but not marked: it will be re-published (at-least-once)
		}
	}
	return len(events), nil
}
