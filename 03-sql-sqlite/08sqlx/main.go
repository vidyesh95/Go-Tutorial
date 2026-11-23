// Lesson 08: sqlx, database/sql with the boilerplate removed.
//
// github.com/jmoiron/sqlx wraps database/sql (sqlx.DB embeds *sql.DB, so everything from lesson
// 02 still works) and adds:
//   - struct scanning via `db:"column"` tags:  Get (one row), Select (many rows), StructScan
//   - named parameters:  NamedExec / NamedQuery with :field placeholders bound from a struct or map
//   - sqlx.In:  expands a slice into  IN (?, ?, ?)  because database/sql cannot bind a slice
//   - Rebind:  rewrites ? placeholders for the target driver ($1, $2 for PostgreSQL)
//
// Compare with lesson 02: there we wrote Scan(&t.ID, &t.ProjectID, ...) and a for rows.Next()
// loop by hand, in the right column order, for every query. With sqlx, adding a column means
// adding a struct field.
//
// Rules and gotchas:
//   - Column names must map to struct fields; an unmapped column is an error ("missing
//     destination name"). Select only what the struct has, or add `db:"-"` / extra fields.
//   - Without tags sqlx lower-cases field names (AssigneeID -> assigneeid), so always tag.
//   - Select loads ALL rows into memory. For huge results iterate with Queryx + StructScan.
//   - sqlx is still runtime-checked: a typo in SQL or a tag is found when the query runs.
//   - sqlx.In returns a query with ? marks; call db.Rebind(query) before running it so it
//     works on PostgreSQL too. An EMPTY slice makes sqlx.In return an error, so guard it.
//
// In industry: sqlx is a very common middle ground. You still write plain SQL (full control,
// easy to optimise) but skip repetitive scanning code. Teams that want compile-time checked
// queries move on to sqlc (lesson 09).
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	"github.com/jmoiron/sqlx"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

// Task uses `db` tags to map columns to fields. Nullable columns are pointers: nil = NULL.
type Task struct {
	ID         int64   `db:"id"`
	ProjectID  int64   `db:"project_id"`
	AssigneeID *int64  `db:"assignee_id"`
	Title      string  `db:"title"`
	Status     string  `db:"status"`
	Priority   int     `db:"priority"`
	DueDate    *string `db:"due_date"`
}

func (t Task) String() string {
	assignee, due := "-", "-"
	if t.AssigneeID != nil {
		assignee = fmt.Sprint(*t.AssigneeID)
	}
	if t.DueDate != nil {
		due = *t.DueDate
	}
	return fmt.Sprintf("#%-2d %-24s %-5s p%d assignee=%s due=%s", t.ID, t.Title, t.Status, t.Priority, assignee, due)
}

const cols = `id, project_id, assignee_id, title, status, priority, due_date`

func main() {
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "sqlx-lesson-*")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(dir)

	fmt.Println("== Connect = Open + Ping ==")
	// sqlx.Connect("sqlite", dsn) is sql.Open + PingContext in one call. The driver name must match
	// what the driver registered ("sqlite" for modernc.org/sqlite, imported by sqldb).
	db, err := sqlx.ConnectContext(ctx, "sqlite", sqldb.DSN(filepath.Join(dir, "sqlx.db")))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	if err := sqldb.Setup(ctx, db.DB); err != nil { // db.DB is the embedded *sql.DB
		log.Fatal(err)
	}
	fmt.Println("connected; sqlx.DB wraps *sql.DB, so db.DB works with plain database/sql helpers")

	fmt.Println("\n== Get: exactly one row into a struct ==")
	var t Task
	if err := db.GetContext(ctx, &t, `SELECT `+cols+` FROM tasks WHERE id = ?`, 3); err != nil {
		log.Fatal(err)
	}
	fmt.Println(t)

	fmt.Println("\n== Get on a missing row returns sql.ErrNoRows, same as database/sql ==")
	err = db.GetContext(ctx, &t, `SELECT `+cols+` FROM tasks WHERE id = ?`, 9999)
	fmt.Println("err:", err)

	fmt.Println("\n== Select: many rows into a slice (no Next/Scan/Close loop) ==")
	var tasks []Task
	if err := db.SelectContext(ctx, &tasks,
		`SELECT `+cols+` FROM tasks WHERE project_id = ? ORDER BY id`, 2); err != nil {
		log.Fatal(err)
	}
	for _, t := range tasks {
		fmt.Println(t)
	}

	fmt.Println("\n== Select a single column into a []string ==")
	var emails []string
	if err := db.SelectContext(ctx, &emails, `SELECT email FROM users ORDER BY id`); err != nil {
		log.Fatal(err)
	}
	fmt.Println(emails)

	fmt.Println("\n== NamedExec with a struct (placeholders :title, :project_id ... come from db tags) ==")
	newTask := Task{ProjectID: 1, Title: "Named insert", Status: "todo", Priority: 2}
	res, err := db.NamedExecContext(ctx,
		`INSERT INTO tasks (project_id, assignee_id, title, status, priority, due_date)
		 VALUES (:project_id, :assignee_id, :title, :status, :priority, :due_date)`, newTask)
	if err != nil {
		log.Fatal(err)
	}
	id, _ := res.LastInsertId()
	fmt.Println("inserted id", id, "(nil pointers were written as NULL)")

	fmt.Println("\n== NamedExec with a map ==")
	res, err = db.NamedExecContext(ctx,
		`UPDATE tasks SET status = :status WHERE project_id = :project AND priority <= :maxprio`,
		map[string]any{"status": "doing", "project": 3, "maxprio": 1})
	if err != nil {
		log.Fatal(err)
	}
	n, _ := res.RowsAffected()
	fmt.Println("rows updated:", n)

	fmt.Println("\n== NamedQuery + StructScan (row by row) ==")
	rows, err := db.NamedQueryContext(ctx,
		`SELECT `+cols+` FROM tasks WHERE project_id = :p AND status = :s ORDER BY id`,
		map[string]any{"p": 3, "s": "doing"})
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var t Task
		if err := rows.StructScan(&t); err != nil {
			log.Fatal(err)
		}
		fmt.Println(t)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}

	fmt.Println("\n== sqlx.In: WHERE id IN (?) with a slice, then Rebind ==")
	ids := []int64{1, 5, 9, 12}
	query, args, err := sqlx.In(`SELECT `+cols+` FROM tasks WHERE id IN (?) ORDER BY id`, ids)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("expanded SQL:", query)
	query = db.Rebind(query) // no-op for SQLite's ?; becomes $1,$2,... on PostgreSQL ("pgx")
	var picked []Task
	if err := db.SelectContext(ctx, &picked, query, args...); err != nil {
		log.Fatal(err)
	}
	for _, t := range picked {
		fmt.Println(t)
	}
	_, _, err = sqlx.In(`SELECT id FROM tasks WHERE id IN (?)`, []int64{})
	fmt.Println("empty slice ->", err, "(check len(ids) == 0 first)")

	fmt.Println("\n== database/sql (lesson 02) vs sqlx ==")
	fmt.Println("database/sql: QueryContext + defer Close + for Next { Scan(&a,&b,...) } + Err()")
	fmt.Println("sqlx        : SelectContext(ctx, &slice, query, args...)")
}
