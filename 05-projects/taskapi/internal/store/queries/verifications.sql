-- name: CreateVerification :exec
INSERT INTO verification (id, identifier, value, expires_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?);

-- name: DeleteVerificationsByIdentifier :exec
DELETE FROM verification WHERE identifier = ?;

-- Atomic "check and burn": a single DELETE ... RETURNING is the whole single-use guarantee. Two
-- concurrent requests with the same token cannot both match, so exactly one wins. The identifier
-- pattern ('email-verification:%') makes sure a password-reset token cannot be used as an
-- email-verification token and vice versa. Expired rows do not match and are left for cleanup.
-- name: ConsumeVerification :one
DELETE FROM verification
WHERE value = sqlc.arg(value) AND identifier LIKE sqlc.arg(identifier_pattern) AND expires_at > sqlc.arg(now)
RETURNING identifier;

-- name: DeleteExpiredVerifications :execrows
DELETE FROM verification WHERE expires_at <= sqlc.arg(now);
