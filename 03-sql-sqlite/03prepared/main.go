// Lesson 03: placeholders, prepared statements, and SQL injection.
//
// NEVER build SQL by concatenating user input into the string. Use placeholders (`?` in SQLite
// and MySQL, `$1, $2` in PostgreSQL) and pass the values separately:
//
//	db.QueryContext(ctx, "SELECT ... WHERE email = ?", email)
//
// The driver sends the SQL text and the values separately, so the database never parses user
// input as SQL. That single habit prevents SQL injection, the most common database attack.
//
// Prepared statements (db.PrepareContext) parse and plan a query once and let you execute it many
// times with different arguments. They help in loops and hot paths. Notes:
//   - A *sql.Stmt is tied to the pool, not one connection; database/sql re-prepares transparently.
//   - ALWAYS Close a Stmt you prepared when you are done with it.
//   - Plain db.QueryContext(ctx, sql, args...) with placeholders is already safe. You do not need
//     Prepare for safety, only for reuse.
//
// What placeholders can NOT do: they stand in for VALUES only, never identifiers or keywords.
// You cannot parameterize a table name, a column name, or ASC/DESC. For a dynamic ORDER BY, map
// the user's choice to a fixed string through a whitelist (a map) and splice only that string in.
//
// In industry: code review rejects any fmt.Sprintf that builds SQL from request data. Code
// generators (sqlc) and query builders make the safe path the default. Linters such as gosec
// flag string-built queries.
package main

import (
	"context"
	"database/sql"
	"fmt"
	"log"

	"github.com/vidyesh95/Go-Tutorial/03-sql-sqlite/internal/sqldb"
)

func main() {
	ctx := context.Background()
	db, cleanup, err := sqldb.OpenTemp(ctx)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()

	fmt.Println("== Placeholders ==")
	var name string
	err = db.QueryRowContext(ctx, `SELECT name FROM users WHERE email = ? AND id > ?`, "ben@example.com", 0).Scan(&name)
	fmt.Println("found:", name, "err:", err)

	fmt.Println("\n== PrepareContext: parse once, execute many times ==")
	stmt, err := db.PrepareContext(ctx, `SELECT title FROM tasks WHERE project_id = ? AND status = ? ORDER BY id`)
	if err != nil {
		log.Fatal(err)
	}
	defer stmt.Close() // forgetting this leaks the prepared statement
	for _, project := range []int{1, 2, 3} {
		rows, err := stmt.QueryContext(ctx, project, "todo")
		if err != nil {
			log.Fatal(err)
		}
		fmt.Printf("project %d todo: %v\n", project, firstColumn(rows))
	}

	fmt.Println("\n== SQL injection demo ==")
	// An attacker types this into a login form's email field:
	evil := `' OR '1'='1`
	fmt.Printf("attacker input: %s\n", evil)

	// VULNERABLE: the input becomes part of the SQL text.
	bad := "SELECT email FROM users WHERE email = '" + evil + "'"
	fmt.Println("\nvulnerable SQL text:", bad)
	fmt.Println("rows returned      :", query(ctx, db, bad))
	fmt.Println("-> the WHERE clause became  email = '' OR '1'='1'  which is true for EVERY row.")

	// SAFE: same intent with a placeholder. The whole input is treated as one string value.
	safe := "SELECT email FROM users WHERE email = ?"
	fmt.Println("\nsafe SQL text      :", safe, "  with arg:", evil)
	fmt.Println("rows returned      :", query(ctx, db, safe, evil))
	fmt.Println("-> no user has that literal email, so nothing matches.")

	fmt.Println("\n== Dynamic ORDER BY must be whitelisted, not parameterized ==")
	for _, userChoice := range []string{"priority", "due", "title; DROP TABLE tasks"} {
		q, ok := orderedTasksQuery(userChoice)
		if !ok {
			fmt.Printf("sort=%q -> rejected (not in whitelist)\n", userChoice)
			continue
		}
		fmt.Printf("sort=%q -> %s\n", userChoice, q)
		fmt.Println("   first 3:", query(ctx, db, q, 3))
	}

	// A placeholder in ORDER BY is just a constant string value, so it silently does NOT sort.
	fmt.Println("\nORDER BY ? (placeholder) sorts by a constant string, so it has no effect:")
	fmt.Println("   first 3:", query(ctx, db, `SELECT title FROM tasks ORDER BY ? LIMIT 3`, "priority"))
}

// query runs a SELECT and returns the first column of every row as a string.
func query(ctx context.Context, db *sql.DB, q string, args ...any) []string {
	rows, err := db.QueryContext(ctx, q, args...)
	if err != nil {
		log.Fatal(err)
	}
	return firstColumn(rows)
}

// firstColumn drains rows (closing them) into a slice of strings.
func firstColumn(rows *sql.Rows) []string {
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			log.Fatal(err)
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	return out
}

// orderedTasksQuery maps user input to a SAFE, fixed ORDER BY clause. Only the strings on the
// right-hand side of this map ever reach the database.
func orderedTasksQuery(userChoice string) (string, bool) {
	orderBy, ok := map[string]string{
		"priority": "priority ASC, id ASC",
		"due":      "due_date IS NULL, due_date ASC", // NULLs last
		"newest":   "id DESC",
	}[userChoice]
	if !ok {
		return "", false
	}
	return "SELECT title FROM tasks ORDER BY " + orderBy + " LIMIT ?", true
}
