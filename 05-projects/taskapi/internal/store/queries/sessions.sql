-- name: CreateSession :exec
INSERT INTO session (id, user_id, token_hash, expires_at, ip_address, user_agent, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetSessionByTokenHash :one
SELECT * FROM session WHERE token_hash = ?;

-- name: ExtendSession :execrows
UPDATE session SET expires_at = ?, updated_at = ? WHERE id = ?;

-- name: ListActiveSessionsByUser :many
SELECT * FROM session WHERE user_id = sqlc.arg(user_id) AND expires_at > sqlc.arg(now) ORDER BY created_at DESC, id DESC;

-- name: DeleteSessionByID :execrows
DELETE FROM session WHERE id = ?;

-- name: DeleteSessionByTokenHash :execrows
DELETE FROM session WHERE token_hash = ?;

-- name: DeleteUserSession :execrows
DELETE FROM session WHERE id = ? AND user_id = ?;

-- name: DeleteOtherSessions :execrows
DELETE FROM session WHERE user_id = ? AND id <> ?;

-- name: DeleteSessionsByUser :execrows
DELETE FROM session WHERE user_id = ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM session WHERE expires_at <= sqlc.arg(now);
