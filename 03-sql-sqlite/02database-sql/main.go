// Lesson 02: database/sql, Go's standard database API.
//
// database/sql is a thin, driver-agnostic layer. You import a driver for its side effect
// (`_ "modernc.org/sqlite"` registers the name "sqlite"), then talk only to *sql.DB.
//
// Key facts:
//   - sql.Open does NOT connect. It validates arguments and returns a lazy handle.
//     Call db.PingContext to prove the database is reachable (do it once at startup).
//   - *sql.DB is NOT a connection. It is a concurrency-safe POOL. Create one per database,
//     share it everywhere, and close it only at program shutdown.
//   - ExecContext   -> statements with no rows back (INSERT/UPDATE/DELETE), returns sql.Result.
//   - QueryContext  -> many rows; you MUST rows.Close() (defer it) and check rows.Err() after the loop.
//   - QueryRowContext -> exactly one row; errors surface at .Scan(); no row = sql.ErrNoRows.
//   - Always pass a context so queries are cancelled on timeout / client disconnect.
//   - NULL handling: scanning NULL into a plain string/int fails. Use sql.Null[T] (generic,
//     Go 1.22+), sql.NullString, or a pointer (*string). Pick one style and stay consistent.
//   - RETURNING id (SQLite 3.35+, PostgreSQL) is the portable way to get a new id.
//     res.LastInsertId() works in SQLite/MySQL but NOT in PostgreSQL drivers.
//
// Connection pool settings (defaults: unlimited open, 2 idle, no lifetime limit):
//   - SetMaxOpenConns   cap on total connections. Protects the database from your app.
//     SQLite allows one writer at a time, so small numbers are fine here.
//   - SetMaxIdleConns   connections kept warm for reuse. Too low = constant reconnecting.
//   - SetConnMaxLifetime / SetConnMaxIdleTime  recycle connections so load balancers,
//     failovers and server-side timeouts do not leave you holding dead ones.
//
// Gotchas:
//   - Forgetting rows.Close() leaks a connection; the pool eventually runs dry and hangs.
//   - Ignoring rows.Err() hides errors that happen mid-iteration (network drop, cancelled ctx).
//   - Do not use Query for INSERT/UPDATE; use Exec.
//
// In industry: services open one *sql.DB at startup, ping it, set pool limits from config
// (often MaxOpen around 10-25 per instance, because instances x MaxOpen must stay below the
// database's connection limit), and hide queries behind a repository / store type.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

// Task mirrors the tasks table. Nullable columns use sql.Null types so NULL is representable.
type Task struct {
	ID         int64
	ProjectID  int64
	AssigneeID sql.Null[int64] // NULL = unassigned
	Title      string
	Status     string
	Priority   int
	DueDate    sql.NullString // NULL = no deadline (stored as ISO-8601 text)
}

func (t Task) String() string {
	assignee := "unassigned"
	if t.AssigneeID.Valid {
		assignee = fmt.Sprintf("user %d", t.AssigneeID.V)
	}
	due := "no due date"
	if t.DueDate.Valid {
		due = "due " + t.DueDate.String
	}
	return fmt.Sprintf("#%-3d %-26s %-5s p%d  %s, %s", t.ID, t.Title, t.Status, t.Priority, assignee, due)
}

const taskColumns = `id, project_id, assignee_id, title, status, priority, due_date`

// scanTask is shared by QueryRow and Query: both *sql.Row and *sql.Rows have Scan.
func scanTask(s interface{ Scan(...any) error }) (Task, error) {
	var t Task
	err := s.Scan(&t.ID, &t.ProjectID, &t.AssigneeID, &t.Title, &t.Status, &t.Priority, &t.DueDate)
	return t, err
}

func main() {
	ctx := context.Background()

	// sql.Open is lazy: this succeeds even for a path that does not exist yet.
	// sqldb.Open wraps sql.Open + PingContext (see internal/sqldb).
	db, cleanup, err := sqldb.OpenTemp(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	fmt.Println("== Pool settings ==")
	db.SetMaxOpenConns(5)                   // never more than 5 connections to the DB
	db.SetMaxIdleConns(5)                   // keep them all warm (idle <= open)
	db.SetConnMaxLifetime(30 * time.Minute) // recycle so stale connections do not pile up
	db.SetConnMaxIdleTime(5 * time.Minute)
	fmt.Printf("stats: %+v\n", db.Stats())

	// A per-operation timeout: the standard way to bound database work.
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	fmt.Println("\n== PingContext ==")
	fmt.Println("ping error:", db.PingContext(ctx))

	fmt.Println("\n== QueryRowContext + Scan (one row) ==")
	task, err := scanTask(db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, 3))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(task)

	fmt.Println("\n== sql.ErrNoRows ==")
	_, err = scanTask(db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, 9999))
	// Always compare with errors.Is, never ==, because layers may wrap the error.
	fmt.Println("errors.Is(err, sql.ErrNoRows):", errors.Is(err, sql.ErrNoRows), "| err:", err)

	fmt.Println("\n== QueryContext (many rows): close and check Err ==")
	rows, err := db.QueryContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE project_id = ? ORDER BY id`, 2)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close() // releases the connection even if we return early
	var tasks []Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			log.Fatal(err)
		}
		tasks = append(tasks, t)
	}
	if err := rows.Err(); err != nil { // errors that ended the iteration early
		log.Fatal(err)
	}
	for _, t := range tasks {
		fmt.Println(t)
	}

	fmt.Println("\n== NULL handling: sql.Null[T], sql.NullString, and a pointer ==")
	var assignee sql.Null[int64]
	var due sql.NullString
	var dueAsPtr *string // pointers also work: nil means NULL
	err = db.QueryRowContext(ctx, `SELECT assignee_id, due_date, due_date FROM tasks WHERE id = 3`).
		Scan(&assignee, &due, &dueAsPtr)
	fmt.Printf("task 3 -> assignee=%+v due=%+v dueAsPtr==nil:%v err=%v\n", assignee, due, dueAsPtr == nil, err)

	// Scanning NULL into a plain int64 is an error, which is why the Null types exist.
	var plain int64
	err = db.QueryRowContext(ctx, `SELECT assignee_id FROM tasks WHERE id = 3`).Scan(&plain)
	fmt.Println("scan NULL into int64:", err)

	// Writing NULL: pass a Null value with Valid=false (or a nil pointer / nil).
	_, err = db.ExecContext(ctx, `UPDATE tasks SET assignee_id = ? WHERE id = ?`, sql.Null[int64]{}, 4)
	fmt.Println("set assignee to NULL, err =", err)

	fmt.Println("\n== ExecContext: RowsAffected ==")
	res, err := db.ExecContext(ctx, `UPDATE tasks SET status = 'done' WHERE project_id = ? AND status = 'todo'`, 1)
	if err != nil {
		log.Fatal(err)
	}
	n, _ := res.RowsAffected()
	fmt.Println("project 1 todo tasks completed:", n)

	fmt.Println("\n== INSERT: RETURNING id vs LastInsertId ==")
	// RETURNING is the portable choice (PostgreSQL has no LastInsertId).
	var newID int64
	err = db.QueryRowContext(ctx,
		`INSERT INTO tasks (project_id, title, priority) VALUES (?, ?, ?) RETURNING id`,
		1, "Write lesson notes", 2).Scan(&newID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("RETURNING id =", newID)

	res, err = db.ExecContext(ctx, `INSERT INTO tasks (project_id, title) VALUES (?, ?)`, 1, "Second task")
	if err != nil {
		log.Fatal(err)
	}
	lastID, _ := res.LastInsertId()
	fmt.Println("LastInsertId =", lastID)

	fmt.Println("\n== Fetch the inserted task back into the struct ==")
	created, err := scanTask(db.QueryRowContext(ctx, `SELECT `+taskColumns+` FROM tasks WHERE id = ?`, newID))
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(created)

	fmt.Println("\n== Pool stats after all that ==")
	s := db.Stats()
	fmt.Printf("open=%d inUse=%d idle=%d waitCount=%d\n", s.OpenConnections, s.InUse, s.Idle, s.WaitCount)
}
