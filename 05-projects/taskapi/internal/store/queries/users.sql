-- name: CreateUser :exec
INSERT INTO users (id, email, name, email_verified, image, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?);

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ?;

-- name: MarkEmailVerified :execrows
UPDATE users SET email_verified = 1, updated_at = ? WHERE id = ?;
