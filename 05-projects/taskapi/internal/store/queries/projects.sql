-- Every project query that takes an owner_id filters on it IN SQL. Authorization is part of the
-- WHERE clause, so another user's row is indistinguishable from a missing row (404, not 403) and
-- there is no check-then-act gap to race through.

-- name: CreateProject :exec
INSERT INTO projects (id, owner_id, name, created_at, updated_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetProject :one
SELECT * FROM projects WHERE id = ? AND owner_id = ?;

-- name: ListProjects :many
-- Keyset pagination: pass an empty after_id for the first page. ids are UUIDv7, so id order is
-- (roughly) creation order and the primary-key index serves the query.
SELECT * FROM projects
WHERE owner_id = sqlc.arg('owner_id') AND id > sqlc.arg('after_id')
ORDER BY id
LIMIT sqlc.arg('row_limit');

-- name: RenameProject :one
UPDATE projects SET name = ?, updated_at = ?
WHERE id = ? AND owner_id = ?
RETURNING *;

-- name: DeleteProject :execrows
-- Tasks go with it via ON DELETE CASCADE.
DELETE FROM projects WHERE id = ? AND owner_id = ?;
