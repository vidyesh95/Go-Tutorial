package userssvc

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
	"uuid"

	"github.com/vidyesh95/Go-Tutorial/07-microservices/internal/sqlitedb"
)

// Schema is applied at startup. It is idempotent so restarting the service is safe.
const Schema = `
CREATE TABLE IF NOT EXISTS users (
    id         TEXT PRIMARY KEY,
    name       TEXT NOT NULL,
    email      TEXT NOT NULL UNIQUE, -- the UNIQUE index is what makes "email taken" race-free
    created_at TEXT NOT NULL         -- RFC 3339, UTC
);`

// Domain errors. The store returns these instead of SQL errors; the gRPC layer maps them to
// status codes. Keeping that mapping in ONE place (server.go) is what keeps codes consistent.
var (
	ErrNotFound   = errors.New("user not found")
	ErrEmailTaken = errors.New("email already registered")
)

// User is the service's internal model. It is deliberately not the protobuf type: the wire
// contract and the storage model change for different reasons and at different speeds.
type User struct {
	ID        string
	Name      string
	Email     string
	CreatedAt time.Time
}

// Store is users-svc's private persistence. Nothing outside this service touches users.db.
type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

func (s *Store) Create(ctx context.Context, name, email string) (User, error) {
	u := User{
		// UUIDv7 is time-ordered, which keeps B-tree inserts append-mostly (good for indexes).
		ID:        uuid.NewV7().String(),
		Name:      name,
		Email:     email,
		CreatedAt: time.Now().UTC().Truncate(time.Microsecond),
	}
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, name, email, created_at) VALUES (?, ?, ?, ?)`,
		u.ID, u.Name, u.Email, u.CreatedAt.Format(time.RFC3339Nano))
	if sqlitedb.IsUniqueViolation(err) {
		return User{}, ErrEmailTaken
	}
	if err != nil {
		return User{}, fmt.Errorf("insert user: %w", err)
	}
	return u, nil
}

func (s *Store) Get(ctx context.Context, id string) (User, error) {
	var (
		u       User
		created string
	)
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, created_at FROM users WHERE id = ?`, id).
		Scan(&u.ID, &u.Name, &u.Email, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, fmt.Errorf("select user: %w", err)
	}
	if u.CreatedAt, err = time.Parse(time.RFC3339Nano, created); err != nil {
		return User{}, fmt.Errorf("parse created_at %q: %w", created, err)
	}
	return u, nil
}
