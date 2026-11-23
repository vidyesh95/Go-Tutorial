// Lesson 05: the queries you need to know for interviews and real work.
//
// JOINS combine rows from two tables on a condition:
//
//	INNER JOIN  only rows that match on both sides
//	LEFT JOIN   every row from the left table; right-side columns are NULL when nothing matches
//	            (use it for "including those with zero", e.g. users with no tasks)
//
//	users          tasks            INNER: Dara disappears (no tasks)
//	 Asha  ───┬──  t1, t5, t6...    LEFT : Dara stays, task columns are NULL
//	 Dara  ───┘    (nothing)
//
// AGGREGATES collapse many rows into one per group: COUNT, SUM, AVG, MIN, MAX with GROUP BY.
//   - WHERE filters rows BEFORE grouping; HAVING filters groups AFTER aggregation.
//   - Every selected column must be in GROUP BY or inside an aggregate.
//   - Gotcha: LEFT JOIN + WHERE on the right table turns it back into an INNER JOIN. Put that
//     condition in the ON clause instead.
//
// SUBQUERIES and CTEs (WITH) name intermediate results. A CTE reads top-to-bottom, so prefer
// it over deeply nested subqueries.
//
// WINDOW FUNCTIONS compute over related rows WITHOUT collapsing them:
//
//	ROW_NUMBER() OVER (PARTITION BY project_id ORDER BY priority)   rank within each project
//	COUNT(*)     OVER (ORDER BY id)                                  running count
//
// CASE is SQL's if/else; COALESCE(a, b, ...) returns the first non-NULL (default values).
// MANY-TO-MANY needs a join table (task_tags); group_concat folds the related rows into one
// string (PostgreSQL: string_agg).
//
// In industry: analytics/reporting endpoints are mostly these queries. Teams keep them in
// .sql files (sqlc) and check their plans with EXPLAIN (next lessons).
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

	show(ctx, db, "INNER JOIN: tasks with their project (projects without tasks vanish)", `
SELECT p.name AS project, t.title, t.status
FROM tasks t
INNER JOIN projects p ON p.id = t.project_id
WHERE t.priority = 1
ORDER BY p.name, t.id`)

	show(ctx, db, "LEFT JOIN: every user with how many tasks they are assigned (Dara has none)", `
SELECT u.name, count(t.id) AS assigned_tasks
FROM users u
LEFT JOIN tasks t ON t.assignee_id = u.id
GROUP BY u.id
ORDER BY assigned_tasks DESC, u.name`)

	show(ctx, db, "LEFT JOIN: every project, including 'Hiring' which has no tasks", `
SELECT p.name AS project, count(t.id) AS tasks
FROM projects p
LEFT JOIN tasks t ON t.project_id = p.id
GROUP BY p.id
ORDER BY p.id`)

	show(ctx, db, "Anti-join: users with NO tasks (LEFT JOIN ... IS NULL)", `
SELECT u.name
FROM users u
LEFT JOIN tasks t ON t.assignee_id = u.id
WHERE t.id IS NULL`)

	show(ctx, db, "GROUP BY + COUNT/SUM/AVG: workload per project", `
SELECT p.name AS project,
       count(*)                                          AS tasks,
       sum(CASE WHEN t.status = 'done' THEN 1 ELSE 0 END) AS done,
       round(avg(t.priority), 2)                         AS avg_priority
FROM tasks t
JOIN projects p ON p.id = t.project_id
GROUP BY p.id
ORDER BY tasks DESC`)

	show(ctx, db, "HAVING: assignees with at least 4 tasks (WHERE cannot filter on aggregates)", `
SELECT u.name, count(*) AS tasks
FROM tasks t
JOIN users u ON u.id = t.assignee_id
GROUP BY u.id
HAVING count(*) >= 4`)

	show(ctx, db, "Subquery: tasks with above-average priority number (less urgent than average)", `
SELECT id, title, priority
FROM tasks
WHERE priority > (SELECT avg(priority) FROM tasks)
ORDER BY priority DESC, id`)

	show(ctx, db, "CTE (WITH): projects whose open-task count is above the average open count", `
WITH open_per_project AS (
    SELECT project_id, count(*) AS open_tasks
    FROM tasks
    WHERE status <> 'done'
    GROUP BY project_id
)
SELECT p.name, o.open_tasks
FROM open_per_project o
JOIN projects p ON p.id = o.project_id
WHERE o.open_tasks > (SELECT avg(open_tasks) FROM open_per_project)`)

	show(ctx, db, "Window: ROW_NUMBER per project ordered by priority (top 2 per project)", `
SELECT project, title, priority, rn FROM (
    SELECT p.name AS project, t.title, t.priority,
           row_number() OVER (PARTITION BY t.project_id ORDER BY t.priority, t.id) AS rn
    FROM tasks t JOIN projects p ON p.id = t.project_id
)
WHERE rn <= 2
ORDER BY project, rn`)

	show(ctx, db, "Window: running count (note project_size only sees rows that survived WHERE id <= 6)", `
SELECT id, project_id,
       count(*) OVER (ORDER BY id)                         AS running_count,
       count(*) OVER (PARTITION BY project_id)             AS project_size
FROM tasks
WHERE id <= 6`)

	show(ctx, db, "CASE + COALESCE: friendly labels for NULLs and buckets", `
SELECT t.id,
       COALESCE(u.name, '(unassigned)')   AS assignee,
       COALESCE(t.due_date, 'no deadline') AS due,
       CASE WHEN t.priority <= 2 THEN 'high'
            WHEN t.priority = 3  THEN 'normal'
            ELSE 'low' END                AS urgency
FROM tasks t
LEFT JOIN users u ON u.id = t.assignee_id
WHERE t.id IN (1, 3, 5, 8, 12)
ORDER BY t.id`)

	show(ctx, db, "Many-to-many: tags per task through task_tags, folded with group_concat", `
SELECT t.id, t.title, COALESCE(group_concat(g.name, ', '), '-') AS tags
FROM tasks t
LEFT JOIN task_tags tt ON tt.task_id = t.id
LEFT JOIN tags g       ON g.id = tt.tag_id
WHERE t.id <= 7
GROUP BY t.id
ORDER BY t.id`)

	show(ctx, db, "Many-to-many the other way: how many tasks carry each tag", `
SELECT g.name AS tag, count(tt.task_id) AS tasks
FROM tags g
LEFT JOIN task_tags tt ON tt.tag_id = g.id
GROUP BY g.id
ORDER BY tasks DESC, g.name`)
}

// show prints the SQL and its result as an aligned table. Cells are scanned as NullString so
// the helper works for any query; NULL prints as NULL.
func show(ctx context.Context, db *sql.DB, title, query string) {
	fmt.Printf("\n== %s ==\n", title)
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		log.Fatalf("%s: %v", title, err)
	}
	defer rows.Close()
	cols, _ := rows.Columns()

	tw := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, strings.Join(cols, "\t"))
	for rows.Next() {
		cells := make([]sql.NullString, len(cols))
		ptrs := make([]any, len(cols))
		for i := range cells {
			ptrs[i] = &cells[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			log.Fatal(err)
		}
		out := make([]string, len(cols))
		for i, c := range cells {
			out[i] = "NULL"
			if c.Valid {
				out[i] = c.String
			}
		}
		fmt.Fprintln(tw, strings.Join(out, "\t"))
	}
	if err := rows.Err(); err != nil {
		log.Fatal(err)
	}
	tw.Flush()
}
