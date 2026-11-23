// Lesson 04: transactions.
//
// A transaction groups several statements into one all-or-nothing unit (ACID):
//   - Atomicity   all statements succeed (Commit) or none of them do (Rollback)
//   - Consistency constraints hold before and after
//   - Isolation   concurrent transactions do not see each other's half-finished work
//   - Durability  after Commit the data survives a crash
//
// Classic example: transferring money = debit one account + credit another. If the process dies
// between the two statements without a transaction, money vanishes. Here we use the task-tracker
// equivalents: "create a project AND its first tasks" and "move tasks to another project AND
// write an audit row".
//
// Go pattern:
//
//	tx, err := db.BeginTx(ctx, nil)
//	if err != nil { ... }
//	defer tx.Rollback()      // safe no-op after Commit; guarantees cleanup on every early return
//	... tx.ExecContext / tx.QueryContext ...
//	return tx.Commit()
//
// Rules and gotchas:
//   - Use the *sql.Tx for EVERYTHING inside the transaction. A call on db instead runs on a
//     different connection, outside the transaction (and with SQLite, may deadlock on the write lock).
//   - Keep transactions short: no HTTP calls or sleeps inside; locks are held until the end.
//   - Cancelling the ctx passed to BeginTx rolls the transaction back automatically.
//   - After a failed statement, decide: return the error (rollback) or handle it and continue.
//   - Rolling back does not undo Go variables, only database changes.
//
// Isolation levels (sql.TxOptions{Isolation: ...}): Read Uncommitted, Read Committed,
// Repeatable Read, Serializable. PostgreSQL defaults to Read Committed and supports the stricter
// ones (Serializable can fail with a retryable error, SQLSTATE 40001, so retry the whole tx).
// SQLite is always effectively serializable (one writer at a time) and this driver accepts the
// options without enforcing them, so you can only SEE the difference on PostgreSQL. There,
// TxOptions{ReadOnly: true} makes writes fail and lets the database optimise.
//
// In industry: every service has a helper like withTx below so handlers cannot forget Rollback
// or Commit. Services that retry on serialization failures / deadlocks wrap that helper in a
// retry loop with backoff.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

// withTx runs fn inside a transaction. If fn returns an error or panics, everything is rolled
// back; otherwise it is committed. Handlers never touch Begin/Commit/Rollback directly.
func withTx(ctx context.Context, db *sql.DB, opts *sql.TxOptions, fn func(tx *sql.Tx) error) (err error) {
	tx, err := db.BeginTx(ctx, opts)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback()
			panic(p) // rolled back; let the panic continue upward
		}
	}()
	if err := fn(tx); err != nil {
		if rbErr := tx.Rollback(); rbErr != nil {
			// Report both: the original failure is the important one.
			return errors.Join(err, fmt.Errorf("rollback: %w", rbErr))
		}
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit: %w", err)
	}
	return nil
}

type newTask struct {
	Title    string
	Priority int
}

// createProjectWithTasks inserts a project and all its tasks atomically.
func createProjectWithTasks(ctx context.Context, db *sql.DB, ownerID int64, name string, tasks []newTask) (int64, error) {
	var projectID int64
	err := withTx(ctx, db, nil, func(tx *sql.Tx) error {
		err := tx.QueryRowContext(ctx,
			`INSERT INTO projects (owner_id, name) VALUES (?, ?) RETURNING id`, ownerID, name).Scan(&projectID)
		if err != nil {
			return fmt.Errorf("insert project: %w", err)
		}
		for _, t := range tasks {
			_, err := tx.ExecContext(ctx,
				`INSERT INTO tasks (project_id, title, priority) VALUES (?, ?, ?)`, projectID, t.Title, t.Priority)
			if err != nil {
				return fmt.Errorf("insert task %q: %w", t.Title, err)
			}
		}
		return nil
	})
	return projectID, err
}

// moveOpenTasks moves every non-done task from one project to another and records an audit row.
// Either all of it happens or none of it does.
func moveOpenTasks(ctx context.Context, db *sql.DB, from, to int64) (int64, error) {
	var moved int64
	err := withTx(ctx, db, nil, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(ctx,
			`UPDATE tasks SET project_id = ? WHERE project_id = ? AND status <> 'done'`, to, from)
		if err != nil {
			return fmt.Errorf("move tasks: %w", err)
		}
		moved, _ = res.RowsAffected()
		_, err = tx.ExecContext(ctx,
			`INSERT INTO audit_log (action, detail) VALUES ('move_tasks', ?)`,
			fmt.Sprintf("moved %d task(s) from project %d to %d", moved, from, to))
		if err != nil {
			return fmt.Errorf("write audit: %w", err)
		}
		return nil
	})
	return moved, err
}

func count(ctx context.Context, db *sql.DB, query string, args ...any) int {
	var n int
	if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		log.Fatal(err)
	}
	return n
}

func main() {
	ctx := context.Background()
	db, cleanup, err := sqldb.OpenTemp(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	// Lesson-local table for the audit example (not part of the shared schema).
	_, err = db.ExecContext(ctx, `CREATE TABLE audit_log (
		id INTEGER PRIMARY KEY, action TEXT NOT NULL, detail TEXT NOT NULL,
		created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ','now')))`)
	if err != nil {
		log.Fatal(err)
	}

	fmt.Println("== 1. Commit: create a project with tasks atomically ==")
	id, err := createProjectWithTasks(ctx, db, 4, "Onboarding", []newTask{
		{"Prepare laptop", 2}, {"Create accounts", 1}, {"Schedule intro calls", 3},
	})
	fmt.Printf("project id=%d err=%v tasks=%d\n", id, err,
		count(ctx, db, `SELECT count(*) FROM tasks WHERE project_id = ?`, id))

	fmt.Println("\n== 2. Rollback: the 3rd task violates CHECK (priority 9), nothing is saved ==")
	before := count(ctx, db, `SELECT count(*) FROM projects`)
	_, err = createProjectWithTasks(ctx, db, 4, "Doomed project", []newTask{
		{"Fine task", 2}, {"Also fine", 3}, {"Bad priority", 9},
	})
	fmt.Println("error:", err)
	fmt.Printf("projects before=%d after=%d (unchanged: the project insert was rolled back too)\n",
		before, count(ctx, db, `SELECT count(*) FROM projects`))
	fmt.Println("tasks named 'Fine task':", count(ctx, db, `SELECT count(*) FROM tasks WHERE title = 'Fine task'`))

	fmt.Println("\n== 3. Move tasks between projects + audit row (atomic) ==")
	moved, err := moveOpenTasks(ctx, db, 2, 1)
	fmt.Printf("moved=%d err=%v audit rows=%d\n", moved, err, count(ctx, db, `SELECT count(*) FROM audit_log`))

	fmt.Println("\n== 4. Failure mid-way: moving to a project that does not exist ==")
	open := count(ctx, db, `SELECT count(*) FROM tasks WHERE project_id = 1`)
	_, err = moveOpenTasks(ctx, db, 1, 999) // FK violation on the UPDATE
	fmt.Println("error:", err)
	fmt.Printf("project 1 tasks before=%d after=%d, audit rows still %d\n",
		open, count(ctx, db, `SELECT count(*) FROM tasks WHERE project_id = 1`),
		count(ctx, db, `SELECT count(*) FROM audit_log`))

	fmt.Println("\n== 5. Manual pattern: defer tx.Rollback() ==")
	err = func() error {
		tx, err := db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		defer tx.Rollback() // no-op if Commit already succeeded
		if _, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'done' WHERE id = 4`); err != nil {
			return err
		}
		// Reading inside the tx sees our own uncommitted change...
		var status string
		_ = tx.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = 4`).Scan(&status)
		fmt.Println("inside tx, task 4 status:", status)
		return errors.New("changed my mind") // ...and returning an error discards it
	}()
	fmt.Println("manual tx result:", err)
	var status string
	_ = db.QueryRowContext(ctx, `SELECT status FROM tasks WHERE id = 4`).Scan(&status)
	fmt.Println("after rollback, task 4 status:", status)

	fmt.Println("\n== 6. TxOptions ==")
	// Options are accepted, but SQLite does not enforce them. On PostgreSQL the write below
	// would fail with "cannot execute UPDATE in a read-only transaction".
	err = withTx(ctx, db, &sql.TxOptions{ReadOnly: true}, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `UPDATE tasks SET status = 'done' WHERE id = 1`)
		return err
	})
	fmt.Println("write inside ReadOnly tx (SQLite does not enforce):", err)

	// SQLite only has serializable behaviour; asking for another level changes nothing here.
	err = withTx(ctx, db, &sql.TxOptions{Isolation: sql.LevelReadCommitted}, func(tx *sql.Tx) error { return nil })
	fmt.Println("LevelReadCommitted on SQLite:", err)
	err = withTx(ctx, db, &sql.TxOptions{Isolation: sql.LevelSerializable}, func(tx *sql.Tx) error { return nil })
	fmt.Println("LevelSerializable on SQLite :", err)
}
