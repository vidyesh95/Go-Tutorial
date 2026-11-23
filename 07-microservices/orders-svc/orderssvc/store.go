package orderssvc

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"uuid"
)

// Schema is applied at startup (idempotent).
//
// Note what is NOT here: a users table or a FOREIGN KEY on orders.user_id. Users live in another
// service's database, so the database cannot enforce that a user exists. The application checks
// it by calling users-svc (see UsersClient). That lost guarantee is the real price of
// "database per service": referential integrity across services is now your code's job.
const Schema = `
CREATE TABLE IF NOT EXISTS orders (
    id         TEXT PRIMARY KEY,
    user_id    TEXT NOT NULL,
    item       TEXT NOT NULL,
    quantity   INTEGER NOT NULL CHECK (quantity > 0),
    status     TEXT NOT NULL,
    created_at TEXT NOT NULL
);

-- One row per Idempotency-Key we have processed, pointing at the order it produced.
-- request_hash detects a client re-using a key for a DIFFERENT request (a client bug).
CREATE TABLE IF NOT EXISTS idempotency_keys (
    key          TEXT PRIMARY KEY,
    request_hash TEXT NOT NULL,
    order_id     TEXT NOT NULL REFERENCES orders(id),
    created_at   TEXT NOT NULL
);

-- The transactional outbox: events to publish, written in the SAME transaction as the order.
-- sent_at stays NULL until the relay has handed the event to the message bus.
CREATE TABLE IF NOT EXISTS outbox (
    id         INTEGER PRIMARY KEY AUTOINCREMENT, -- publish order
    event_id   TEXT NOT NULL,                     -- stable id consumers use to de-duplicate
    topic      TEXT NOT NULL,
    payload    TEXT NOT NULL,                     -- JSON
    created_at TEXT NOT NULL,
    sent_at    TEXT
);
-- Partial index: only unsent rows are indexed, so polling stays fast however big the table gets.
CREATE INDEX IF NOT EXISTS outbox_unsent ON outbox (id) WHERE sent_at IS NULL;`

// Errors the store and service layers return; api.go maps them to HTTP statuses.
var (
	ErrOrderNotFound = errors.New("order not found")
	ErrKeyReuse      = errors.New("idempotency key reused with a different request")
)

// TopicOrderCreated is the event topic published when an order is created.
const TopicOrderCreated = "orders.created"

// Order is both the domain model and the JSON response body.
type Order struct {
	ID        string    `json:"id"`
	UserID    string    `json:"user_id"`
	Item      string    `json:"item"`
	Quantity  int       `json:"quantity"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

// Store is orders-svc's private persistence.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// Ping reports whether the database is reachable (used by /healthz).
func (s *Store) Ping(ctx context.Context) error { return s.db.PingContext(ctx) }

// queryer is the read half shared by *sql.DB and *sql.Tx, so the same lookup code runs both
// outside a transaction (fast path) and inside one (the authoritative re-check).
type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

const orderCols = `id, user_id, item, quantity, status, created_at`

func scanOrder(row *sql.Row) (Order, error) {
	var (
		o       Order
		created string
	)
	if err := row.Scan(&o.ID, &o.UserID, &o.Item, &o.Quantity, &o.Status, &created); err != nil {
		return Order{}, err
	}
	var err error
	o.CreatedAt, err = time.Parse(time.RFC3339Nano, created)
	return o, err
}

// Get returns one order or ErrOrderNotFound.
func (s *Store) Get(ctx context.Context, id string) (Order, error) {
	o, err := scanOrder(s.db.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, ErrOrderNotFound
	}
	return o, err
}

// Replay looks for an earlier order created with this idempotency key.
//   - not seen before: found=false
//   - seen with the same request hash: found=true and the ORIGINAL order is returned
//   - seen with a different hash: ErrKeyReuse
func (s *Store) Replay(ctx context.Context, key, hash string) (Order, bool, error) {
	return replay(ctx, s.db, key, hash)
}

func replay(ctx context.Context, q queryer, key, hash string) (Order, bool, error) {
	var storedHash, orderID string
	err := q.QueryRowContext(ctx,
		`SELECT request_hash, order_id FROM idempotency_keys WHERE key = ?`, key).Scan(&storedHash, &orderID)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, false, nil
	}
	if err != nil {
		return Order{}, false, fmt.Errorf("lookup idempotency key: %w", err)
	}
	if storedHash != hash {
		return Order{}, false, ErrKeyReuse
	}
	o, err := scanOrder(q.QueryRowContext(ctx, `SELECT `+orderCols+` FROM orders WHERE id = ?`, orderID))
	if err != nil {
		return Order{}, false, fmt.Errorf("load replayed order: %w", err)
	}
	return o, true, nil
}

// orderCreated is the payload of the orders.created event.
type orderCreated struct {
	OrderID  string `json:"order_id"`
	UserID   string `json:"user_id"`
	Item     string `json:"item"`
	Quantity int    `json:"quantity"`
}

// CreateOrder atomically stores the order, its idempotency record and its outbox event.
// replayed reports that the key was already used (by a concurrent request that won the race)
// and the earlier order is returned instead.
//
// THE OUTBOX PATTERN: we must save the order AND announce "order created". Doing both as two
// separate steps (INSERT, then publish to Kafka) cannot be made reliable: crash between them
// and either the event is lost (order exists, nobody told) or, if you publish first, a phantom
// event exists for an order that never committed. A database cannot share a transaction with a
// message broker. So we write the event to an `outbox` table in the SAME database transaction
// as the order: both commit or neither does. A separate relay (outbox.go) later reads the
// table and publishes. Result: the event is delivered at least once, never lost, never phantom.
func (s *Store) CreateOrder(ctx context.Context, key, hash string, o Order) (_ Order, replayed bool, err error) {
	// The DSN uses _txlock=immediate, so this takes the write lock immediately and two requests
	// racing with the same key are serialised: the second sees the first's row below.
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, false, fmt.Errorf("begin: %w", err)
	}
	defer tx.Rollback() // no-op after Commit; undoes everything on any early return

	if prev, found, err := replay(ctx, tx, key, hash); err != nil || found {
		return prev, found, err
	}

	o.ID = uuid.NewV7().String()
	o.Status = "created"
	o.CreatedAt = time.Now().UTC().Truncate(time.Microsecond)
	created := o.CreatedAt.Format(time.RFC3339Nano)
	payload, err := json.Marshal(orderCreated{OrderID: o.ID, UserID: o.UserID, Item: o.Item, Quantity: o.Quantity})
	if err != nil {
		return Order{}, false, err
	}

	if _, err := tx.ExecContext(ctx,
		`INSERT INTO orders (`+orderCols+`) VALUES (?, ?, ?, ?, ?, ?)`,
		o.ID, o.UserID, o.Item, o.Quantity, o.Status, created); err != nil {
		return Order{}, false, fmt.Errorf("insert order: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO idempotency_keys (key, request_hash, order_id, created_at) VALUES (?, ?, ?, ?)`,
		key, hash, o.ID, created); err != nil {
		return Order{}, false, fmt.Errorf("insert idempotency key: %w", err)
	}
	if _, err := tx.ExecContext(ctx,
		`INSERT INTO outbox (event_id, topic, payload, created_at) VALUES (?, ?, ?, ?)`,
		uuid.NewV7().String(), TopicOrderCreated, string(payload), created); err != nil {
		return Order{}, false, fmt.Errorf("insert outbox event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Order{}, false, fmt.Errorf("commit: %w", err)
	}
	return o, false, nil
}

// pendingEvent is an outbox row not yet published.
type pendingEvent struct {
	seq   int64
	event Event
}

// pending returns up to limit unsent events, oldest first.
func (s *Store) pending(ctx context.Context, limit int) ([]pendingEvent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, event_id, topic, payload FROM outbox WHERE sent_at IS NULL ORDER BY id LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("select outbox: %w", err)
	}
	defer rows.Close()
	var out []pendingEvent
	for rows.Next() {
		var (
			p       pendingEvent
			payload string
		)
		if err := rows.Scan(&p.seq, &p.event.ID, &p.event.Topic, &payload); err != nil {
			return nil, err
		}
		p.event.Payload = []byte(payload)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *Store) markSent(ctx context.Context, seq int64) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE outbox SET sent_at = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339Nano), seq)
	if err != nil {
		return fmt.Errorf("mark outbox sent: %w", err)
	}
	return nil
}

// CountOutbox returns how many outbox events are (sent, unsent). Used by tests and the demo.
func (s *Store) CountOutbox(ctx context.Context) (sent, unsent int, err error) {
	err = s.db.QueryRowContext(ctx,
		`SELECT COALESCE(SUM(sent_at IS NOT NULL), 0), COALESCE(SUM(sent_at IS NULL), 0) FROM outbox`).
		Scan(&sent, &unsent)
	return
}
