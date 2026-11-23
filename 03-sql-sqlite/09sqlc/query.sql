-- name: GetTask :one
-- GetTask returns one task by id (sql.ErrNoRows if it does not exist).
SELECT * FROM tasks
WHERE id = ?;

-- name: ListTasksByProject :many
SELECT * FROM tasks
WHERE project_id = ?
ORDER BY priority, id;

-- name: ListTasksFiltered :many
-- Optional filters: pass an invalid sql.Null* (NULL) to ignore a filter. The CAST gives sqlc a
-- type to work with; without it SQLite parameters are generated as interface{}.
SELECT * FROM tasks
WHERE (CAST(sqlc.narg('status') AS TEXT) IS NULL OR status = sqlc.narg('status'))
  AND (CAST(sqlc.narg('assignee_id') AS INTEGER) IS NULL OR assignee_id = sqlc.narg('assignee_id'))
ORDER BY priority, id
LIMIT sqlc.arg('row_limit');

-- name: CreateTask :one
INSERT INTO tasks (project_id, assignee_id, title, status, priority, due_date)
VALUES (?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: UpdateTaskStatus :exec
UPDATE tasks SET status = sqlc.arg('status') WHERE id = sqlc.arg('id');

-- name: DeleteTask :execrows
DELETE FROM tasks WHERE id = ?;

-- name: CountTasksByStatus :many
SELECT status, COUNT(*) AS total
FROM tasks
WHERE project_id = ?
GROUP BY status
ORDER BY status;

-- name: ListProjectsWithTaskCount :many
SELECT p.id, p.name, u.name AS owner_name, COUNT(t.id) AS task_count
FROM projects p
JOIN users u ON u.id = p.owner_id
LEFT JOIN tasks t ON t.project_id = p.id
GROUP BY p.id, p.name, u.name
ORDER BY p.id;
