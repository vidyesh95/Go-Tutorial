-- name: CreateUser :exec
INSERT INTO users (id, email, name, password_hash, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetUserByEmail :one
SELECT * FROM users WHERE email = ?;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = ?;
