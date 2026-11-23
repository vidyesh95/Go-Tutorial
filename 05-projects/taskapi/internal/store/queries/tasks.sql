-- Tasks have no owner column; ownership is the owning project's owner. The subquery form keeps
-- the authorization inside the same statement as the read/write.

-- name: CreateTask :exec
INSERT INTO tasks (id, project_id, title, status, priority, due_date, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetTask :one
SELECT * FROM tasks
WHERE tasks.id = sqlc.arg('id')
  AND tasks.project_id IN (SELECT p.id FROM projects p WHERE p.owner_id = sqlc.arg('owner_id'));

-- name: ListTasks :many
-- The caller has already proven it owns project_id. status is optional: the CAST gives sqlc a
-- type for the nullable parameter (otherwise SQLite parameters come out as interface{}), and
-- passing NULL disables the filter.
SELECT * FROM tasks
WHERE project_id = sqlc.arg('project_id')
  AND (CAST(sqlc.narg('status') AS TEXT) IS NULL OR status = sqlc.narg('status'))
  AND id > sqlc.arg('after_id')
ORDER BY id
LIMIT sqlc.arg('row_limit');

-- name: UpdateTask :one
UPDATE tasks
SET title = sqlc.arg('title'),
    status = sqlc.arg('status'),
    priority = sqlc.arg('priority'),
    due_date = sqlc.arg('due_date'),
    updated_at = sqlc.arg('updated_at')
WHERE tasks.id = sqlc.arg('id')
  AND tasks.project_id IN (SELECT p.id FROM projects p WHERE p.owner_id = sqlc.arg('owner_id'))
RETURNING *;

-- name: DeleteTask :execrows
DELETE FROM tasks
WHERE tasks.id = sqlc.arg('id')
  AND tasks.project_id IN (SELECT p.id FROM projects p WHERE p.owner_id = sqlc.arg('owner_id'));
