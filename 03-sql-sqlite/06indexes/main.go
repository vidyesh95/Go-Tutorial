// Lesson 06: indexes, EXPLAIN QUERY PLAN, and the N+1 query problem.
//
// An index is a sorted copy of one or more columns (a B-tree) that points back to the rows.
// Without one, the database must read EVERY row (SCAN, O(n)). With one it jumps straight to the
// matching rows (SEARCH, O(log n)). Read plans with:
//
//	EXPLAIN QUERY PLAN <query>      (SQLite)      EXPLAIN ANALYZE <query>   (PostgreSQL)
//
// Look for  SCAN tasks  (slow on big tables)  vs  SEARCH tasks USING INDEX idx_name (...).
//
// Rules of thumb:
//   - Index the columns you filter (WHERE), join (ON, i.e. foreign keys), and sort (ORDER BY) on.
//     Neither SQLite nor PostgreSQL index foreign key columns automatically.
//   - COMPOSITE index order matters: an index on (a, b) serves  a = ?  and  a = ? AND b = ?,
//     but NOT  b = ?  alone (the "leftmost prefix" rule). Put equality columns first, the
//     range/sort column last.
//   - A covering index contains every column the query needs, so the table is never touched.
//   - Indexes are not free: each one slows INSERT/UPDATE/DELETE and uses disk. Do not index
//     low-cardinality columns (e.g. a boolean) on their own, and drop unused indexes.
//   - Wrapping the column in a function (WHERE lower(email) = ?) disables a plain index; use an
//     expression index or store the data normalised.
//
// N+1 PROBLEM: one query to list N rows, then one more query per row (e.g. fetch each task's
// project). That is N+1 round trips. SQLite runs inside your process so it looks cheap here, but
// against a networked database every query costs ~0.5 to 2 ms, so 1000 rows = 1+ seconds.
// Fix with a JOIN, or one query using WHERE id IN (...) and then group in Go.
//
// In industry: slow-query logs and APM traces point at missing indexes and N+1 patterns. ORMs
// cause N+1 by accident through lazy loading; the cure is eager loading (JOIN / preload) or
// explicit batched queries.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

func main() {
	ctx := context.Background()
	db, cleanup, err := sqldb.OpenTemp(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	const rowsToInsert = 20000
	fmt.Printf("== Setup: inserting %d generated tasks in ONE transaction ==\n", rowsToInsert)
	start := time.Now()
	insertTasks(ctx, db, rowsToInsert)
	fmt.Printf("inserted in %v (one tx + one prepared statement is how you bulk-load)\n", time.Since(start).Round(time.Millisecond))

	// ---- 1. single-column index ----
	fmt.Println("\n== 1. Filtering on a non-indexed column (due_date) ==")
	q := `SELECT id, title FROM tasks WHERE due_date = '2026-06-15'`
	plan(ctx, db, q)
	before := timeQuery(ctx, db, q, 200)
	fmt.Printf("200 runs: %v\n", before)

	fmt.Println("\nCREATE INDEX idx_tasks_due ON tasks (due_date)")
	mustExec(ctx, db, `CREATE INDEX idx_tasks_due ON tasks (due_date)`)
	plan(ctx, db, q)
	after := timeQuery(ctx, db, q, 200)
	fmt.Printf("200 runs: %v  (about %.0fx faster)\n", after, float64(before)/float64(max(after, 1)))

	// ---- 2. composite index order ----
	fmt.Println("\n== 2. Composite index (project_id, status): leftmost prefix rule ==")
	fmt.Println("(schema.sql already created idx_tasks_project_status ON tasks (project_id, status))")
	for _, q := range []string{
		`SELECT id FROM tasks WHERE project_id = 1 AND status = 'todo'`,
		`SELECT id FROM tasks WHERE project_id = 1`,
		`SELECT id, title FROM tasks WHERE status = 'todo'`,
	} {
		fmt.Println("\nSQL>", q)
		plan(ctx, db, q)
	}
	fmt.Println("\n-> the first two are COVERING because we only select id (every SQLite index implicitly stores the rowid).")
	fmt.Println("-> status alone cannot use the index because status is the SECOND column. For that")
	fmt.Println("   query you would add an index on (status) or reorder based on which filters you need.")

	// ---- 3. covering index ----
	fmt.Println("\n== 3. Covering index: the query is answered from the index alone ==")
	q = `SELECT project_id, status FROM tasks WHERE project_id = 2`
	plan(ctx, db, q)
	fmt.Println("-> 'COVERING INDEX' means no table lookup was needed.")

	// ---- 4. N+1 ----
	fmt.Println("\n== 4. N+1 query problem: project name for each of 2000 tasks ==")
	const n = 2000

	start = time.Now()
	nPlusOne := projectNamesNPlusOne(ctx, db, n)
	t1 := time.Since(start)
	fmt.Printf("N+1  : %d queries, %v\n", n+1, t1.Round(time.Microsecond))

	start = time.Now()
	joined := projectNamesJoin(ctx, db, n)
	t2 := time.Since(start)
	fmt.Printf("JOIN : 1 query,   %v\n", t2.Round(time.Microsecond))

	start = time.Now()
	batched := projectNamesBatched(ctx, db, n)
	t3 := time.Since(start)
	fmt.Printf("IN   : 2 queries, %v  (query 1: tasks, query 2: projects WHERE id IN (...))\n", t3.Round(time.Microsecond))

	fmt.Println("same results:", equal(nPlusOne, joined) && equal(joined, batched))
	fmt.Printf("in-process SQLite hides most of the cost. With a 1ms network round trip the N+1 version\n")
	fmt.Printf("would take about %d ms of latency alone, versus ~2 ms for the JOIN.\n", n+1)
}

// insertTasks bulk-loads generated rows inside a single transaction using one prepared statement.
// Doing 20000 separate autocommit INSERTs would fsync 20000 times and be dramatically slower.
func insertTasks(ctx context.Context, db *sql.DB, n int) {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		log.Fatal(err)
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx,
		`INSERT INTO tasks (project_id, assignee_id, title, status, priority, due_date) VALUES (?, ?, ?, ?, ?, ?)`)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close()

	statuses := []string{"todo", "doing", "done"}
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := range n {
		var assignee any // nil => NULL
		if i%5 != 0 {
			assignee = 1 + i%3
		}
		due := base.AddDate(0, 0, i%365).Format(time.DateOnly)
		_, err := stmt.ExecContext(ctx, 1+i%3, assignee, fmt.Sprintf("Generated task %d", i),
			statuses[i%3], 1+i%5, due)
		if err != nil {
			log.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		log.Fatal(err)
	}
}

// plan prints the "detail" column of EXPLAIN QUERY PLAN.
func plan(ctx context.Context, db *sql.DB, query string) {
	rows, err := db.QueryContext(ctx, "EXPLAIN QUERY PLAN "+query)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			log.Fatal(err)
		}
		fmt.Println("   plan:", detail)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
}

func timeQuery(ctx context.Context, db *sql.DB, query string, runs int) time.Duration {
	start := time.Now()
	for range runs {
		rows, err := db.QueryContext(ctx, query)
		if err != nil {
			log.Fatal(err)
		}
		for rows.Next() {
		}
		rows.Close()
	}
	return time.Since(start)
}

func mustExec(ctx context.Context, db *sql.DB, q string) {
	if _, err := db.ExecContext(ctx, q); err != nil {
		log.Fatal(err)
	}
}

// projectNamesNPlusOne: 1 query for the tasks, then 1 query PER task. This is the anti-pattern.
func projectNamesNPlusOne(ctx context.Context, db *sql.DB, n int) map[int64]string {
	out := make(map[int64]string, n)
	rows, err := db.QueryContext(ctx, `SELECT id, project_id FROM tasks ORDER BY id LIMIT ?`, n)
	if err != nil {
		log.Fatal(err)
	}
	type pair struct{ id, projectID int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.id, &p.projectID); err != nil {
			log.Fatal(err)
		}
		pairs = append(pairs, p)
	}
	rows.Close() // finish reading before issuing more queries
	for _, p := range pairs {
		var name string
		if err := db.QueryRowContext(ctx, `SELECT name FROM projects WHERE id = ?`, p.projectID).Scan(&name); err != nil {
			log.Fatal(err)
		}
		out[p.id] = name
	}
	return out
}

// projectNamesJoin: the database does the matching in one round trip.
func projectNamesJoin(ctx context.Context, db *sql.DB, n int) map[int64]string {
	out := make(map[int64]string, n)
	rows, err := db.QueryContext(ctx,
		`SELECT t.id, p.name FROM tasks t JOIN projects p ON p.id = t.project_id ORDER BY t.id LIMIT ?`, n)
	if err != nil {
		log.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var name string
		if err := rows.Scan(&id, &name); err != nil {
			log.Fatal(err)
		}
		out[id] = name
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	return out
}

// projectNamesBatched: two queries total, the second uses IN (...) with generated placeholders.
// Useful when a JOIN is awkward (e.g. the second query lives in another service or repository).
func projectNamesBatched(ctx context.Context, db *sql.DB, n int) map[int64]string {
	rows, err := db.QueryContext(ctx, `SELECT id, project_id FROM tasks ORDER BY id LIMIT ?`, n)
	if err != nil {
		log.Fatal(err)
	}
	taskProject := make(map[int64]int64, n)
	ids := map[int64]struct{}{}
	for rows.Next() {
		var id, pid int64
		if err := rows.Scan(&id, &pid); err != nil {
			log.Fatal(err)
		}
		taskProject[id] = pid
		ids[pid] = struct{}{}
	}
	rows.Close()

	// Build "?, ?, ?" with one placeholder per distinct project id (values are still bound, not pasted).
	placeholders := strings.TrimSuffix(strings.Repeat("?, ", len(ids)), ", ")
	args := make([]any, 0, len(ids))
	for pid := range ids {
		args = append(args, pid)
	}
	prows, err := db.QueryContext(ctx, `SELECT id, name FROM projects WHERE id IN (`+placeholders+`)`, args...)
	if err != nil {
		log.Fatal(err)
	}
	defer prows.Close()
	names := map[int64]string{}
	for prows.Next() {
		var id int64
		var name string
		if err := prows.Scan(&id, &name); err != nil {
			log.Fatal(err)
		}
		names[id] = name
	}
	out := make(map[int64]string, n)
	for id, pid := range taskProject {
		out[id] = names[pid]
	}
	return out
}

func equal(a, b map[int64]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}
