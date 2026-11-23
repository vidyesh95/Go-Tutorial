-- name: CreateAccount :exec
INSERT INTO account (
    id, user_id, provider_id, account_id, password,
    access_token, refresh_token, id_token, access_token_expires_at, refresh_token_expires_at, scope,
    created_at, updated_at
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetAccountByProvider :one
SELECT * FROM account WHERE provider_id = ? AND account_id = ?;

-- name: GetCredentialAccountByUser :one
SELECT * FROM account WHERE user_id = ? AND provider_id = 'credential';

-- name: UpdateAccountPassword :execrows
UPDATE account SET password = ?, updated_at = ? WHERE id = ?;

-- name: UpdateCredentialPasswordByUser :execrows
UPDATE account SET password = ?, updated_at = ? WHERE user_id = ? AND provider_id = 'credential';

-- name: UpdateAccountTokens :execrows
UPDATE account
SET access_token = ?, refresh_token = ?, id_token = ?,
    access_token_expires_at = ?, refresh_token_expires_at = ?, scope = ?, updated_at = ?
WHERE id = ?;
