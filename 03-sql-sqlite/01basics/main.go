// Lesson 01: SQL basics, run through Go so you can see every statement and its result.
//
// SQL is declarative: you describe WHAT rows you want, and the database decides HOW to find them.
// The statements you need every day:
//
//	CREATE TABLE   define structure (done for us by sqldb.Setup from sql/schema.sql)
//	INSERT         add rows
//	SELECT         read rows        (WHERE filters, ORDER BY sorts, LIMIT caps)
//	UPDATE         change rows      (ALWAYS with a WHERE, or you update every row)
//	DELETE         remove rows      (ALWAYS with a WHERE)
//
// Constraints are rules the DATABASE enforces no matter which program writes the data:
// NOT NULL, UNIQUE, CHECK, PRIMARY KEY, FOREIGN KEY. Validate in Go for friendly messages, but
// keep the constraints: they are the last line of defence against bugs and concurrent writers.
//
// NULL means "unknown / missing", not zero and not empty string. Three-valued logic follows:
//
//	NULL = NULL      -> NULL (not true!)       use  x IS NULL / x IS NOT NULL
//	NULL <> 5        -> NULL                   rows where the condition is NULL are filtered OUT
//	COUNT(col)       ignores NULLs;  COUNT(*) counts rows
//
// Gotchas:
//   - Foreign keys are off by default in SQLite; sqldb.DSN switches them on per connection.
//   - An UPDATE/DELETE that matches zero rows is NOT an error; check RowsAffected.
//
// In industry: engineers write SQL in migrations and in query files, review it like any other
// code, and rely on constraints (UNIQUE email, FK owner) to keep data valid even when
// two services or a one-off script write to the same tables.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

func main() {
	ctx := context.Background()
	db, cleanup, err := sqldb.OpenTemp(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	fmt.Println("== CREATE TABLE (already applied by sqldb.Setup from sql/schema.sql) ==")
	showQuery(ctx, db, "SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name")

	fmt.Println("\n== INSERT ==")
	run(ctx, db, `INSERT INTO users (email, name) VALUES ('eve@example.com', 'Eve')`)
	showQuery(ctx, db, "SELECT id, email, name FROM users ORDER BY id")

	fmt.Println("\n== SELECT with WHERE / ORDER BY / LIMIT ==")
	// Top 3 most urgent open tasks: priority 1 is the highest, ties broken by id.
	showQuery(ctx, db, `SELECT id, title, status, priority FROM tasks
WHERE status <> 'done'
ORDER BY priority ASC, id ASC
LIMIT 3`)

	fmt.Println("\n== UPDATE ==")
	run(ctx, db, `UPDATE tasks SET status = 'doing' WHERE id = 3`)
	showQuery(ctx, db, "SELECT id, title, status FROM tasks WHERE id = 3")

	fmt.Println("\n== DELETE ==")
	run(ctx, db, `DELETE FROM tasks WHERE id = 8`)
	run(ctx, db, `DELETE FROM tasks WHERE id = 999`) // matches nothing: 0 rows, no error

	fmt.Println("\n== NULL semantics ==")
	fmt.Println("(assignee_id is NULL for unassigned tasks)")
	showQuery(ctx, db, `SELECT count(*) AS "assignee_id = NULL" FROM tasks WHERE assignee_id = NULL`)
	showQuery(ctx, db, `SELECT count(*) AS "assignee_id IS NULL" FROM tasks WHERE assignee_id IS NULL`)
	// NOT IN with a NULL in the list is a classic trap: it returns nothing.
	showQuery(ctx, db, `SELECT count(*) AS "x NOT IN (1, NULL)" FROM tasks WHERE id NOT IN (1, NULL)`)
	showQuery(ctx, db, `SELECT count(*) AS "count(*)", count(due_date) AS "count(due_date)" FROM tasks`)

	fmt.Println("\n== Constraint violations are ordinary errors ==")
	attempt(ctx, db, "UNIQUE   (duplicate email)",
		`INSERT INTO users (email, name) VALUES ('asha@example.com', 'Impostor')`)
	attempt(ctx, db, "CHECK    (priority 9 is outside 1..5)",
		`INSERT INTO tasks (project_id, title, priority) VALUES (1, 'bad', 9)`)
	attempt(ctx, db, "CHECK    (status not in the allowed list)",
		`INSERT INTO tasks (project_id, title, status) VALUES (1, 'bad', 'blocked')`)
	attempt(ctx, db, "NOT NULL (title missing)",
		`INSERT INTO tasks (project_id, title) VALUES (1, NULL)`)
	attempt(ctx, db, "FOREIGN KEY (project 42 does not exist)",
		`INSERT INTO tasks (project_id, title) VALUES (42, 'orphan')`)

	fmt.Println("\n== ON DELETE rules from the schema ==")
	// tasks.assignee_id is ON DELETE SET NULL; projects.owner_id is ON DELETE CASCADE.
	run(ctx, db, `DELETE FROM users WHERE id = 3`)
	showQuery(ctx, db, `SELECT count(*) AS "projects owned by Chen (cascade-deleted)" FROM projects WHERE owner_id = 3`)
	showQuery(ctx, db, `SELECT count(*) AS "tasks whose assignee became NULL" FROM tasks WHERE id IN (4, 11, 14) AND assignee_id IS NULL`)
}

// run prints a statement, executes it, and reports rows affected.
func run(ctx context.Context, db *sql.DB, stmt string) {
	fmt.Println("SQL>", oneLine(stmt))
	res, err := db.ExecContext(ctx, stmt)
	if err != nil {
		log.Fatal(err)
	}
	n, _ := res.RowsAffected()
	fmt.Printf("     %d row(s) affected\n", n)
}

// attempt runs a statement that is EXPECTED to fail and prints the error.
func attempt(ctx context.Context, db *sql.DB, label, stmt string) {
	fmt.Println("SQL>", oneLine(stmt))
	_, err := db.ExecContext(ctx, stmt)
	if err == nil {
		fmt.Printf("     %s: unexpectedly succeeded!\n", label)
		return
	}
	fmt.Printf("     %s -> error: %v\n", label, err)
}

// showQuery runs a SELECT and prints the result as an aligned table. It scans every column
// into sql.NullString (works for any column type) just to keep this helper generic.
func showQuery(ctx context.Context, db *sql.DB, query string) {
	fmt.Println("SQL>", oneLine(query))
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()

	cols, err := rows.Columns()
	if err != nil {
		log.Fatal(err)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "     "+strings.Join(cols, "\t"))
	for rows.Next() {
		vals := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			log.Fatal(err)
		}
		cells := make([]string, len(cols))
		for i, v := range vals {
			if v.Valid {
				cells[i] = v.String
			} else {
				cells[i] = "NULL"
			}
		}
		fmt.Fprintln(tw, "     "+strings.Join(cells, "\t"))
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	tw.Flush()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
