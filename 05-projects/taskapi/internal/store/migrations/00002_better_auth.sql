-- +goose Up

-- Better Auth-style authentication schema (https://www.better-auth.com/docs/concepts/database),
-- adapted to SQLite and snake_case. Four tables: user (ours: "users"), account, session,
-- verification. The point of the design: WHO someone is (users) is separate from HOW they prove
-- it (account rows: a password, a GitHub login, ...) and from the logins currently in progress
-- (session rows) and the one-time links we emailed them (verification rows).
--
-- Naming map (Better Auth camelCase -> here): emailVerified -> email_verified,
-- createdAt -> created_at, userId -> user_id, providerId -> provider_id, accountId -> account_id,
-- accessToken -> access_token, refreshToken -> refresh_token, idToken -> id_token,
-- accessTokenExpiresAt -> access_token_expires_at, refreshTokenExpiresAt -> refresh_token_expires_at,
-- expiresAt -> expires_at, ipAddress -> ip_address, userAgent -> user_agent, token -> token_hash.
-- Timestamps stay fixed-width UTC TEXT like in 00001 (see there).

-- 1. Extend users. SQLite can ADD COLUMN only with a constant default for NOT NULL columns, so
--    updated_at gets DEFAULT '' and is backfilled right below; the application always sets it.
ALTER TABLE users ADD COLUMN email_verified INTEGER NOT NULL DEFAULT 0; -- 0/1: SQLite has no boolean
ALTER TABLE users ADD COLUMN image TEXT;                                -- avatar URL, NULL = none
ALTER TABLE users ADD COLUMN updated_at TEXT NOT NULL DEFAULT '';
UPDATE users SET updated_at = created_at;

-- 2. account: one row per way a user can sign in.
--    provider_id 'credential' = email + password (account_id = the user's own id, password set);
--    provider_id 'github'     = Sign in with GitHub (account_id = GitHub's numeric user id).
--    One user can have several accounts ("linked accounts"); (provider_id, account_id) is unique
--    so one GitHub identity can belong to only one user.
CREATE TABLE account (
    id                       TEXT PRIMARY KEY,
    user_id                  TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider_id              TEXT NOT NULL,
    account_id               TEXT NOT NULL,
    password                 TEXT, -- argon2id PHC string; ONLY for 'credential', NULL otherwise
    access_token             TEXT, -- OAuth provider credentials, NULL for 'credential'. Stored
    refresh_token            TEXT, -- as Better Auth does; encrypt at rest before you actually
    id_token                 TEXT, -- USE them for provider API calls (see README).
    access_token_expires_at  TEXT,
    refresh_token_expires_at TEXT,
    scope                    TEXT,
    created_at               TEXT NOT NULL,
    updated_at               TEXT NOT NULL,
    UNIQUE (provider_id, account_id)
);
-- "Which accounts does this user have?" (also serves the ON DELETE CASCADE from users).
CREATE INDEX idx_account_user_id ON account (user_id);

-- 3. session: DB-backed login sessions for cookie clients.
--    DELIBERATE DEVIATION from Better Auth, which stores the raw session token: we store only its
--    SHA-256 (token_hash). The browser holds the random token in a cookie; the database holds a
--    digest that cannot be turned back into a cookie. A leaked database (backup, SQL injection,
--    a read-only replica) therefore does not hand out live sessions.
CREATE TABLE session (
    id         TEXT PRIMARY KEY, -- public handle used by list/revoke; NOT the secret
    user_id    TEXT NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    token_hash TEXT NOT NULL UNIQUE, -- hex SHA-256 of the cookie value; UNIQUE gives the lookup index
    expires_at TEXT NOT NULL,
    ip_address TEXT,
    user_agent TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL -- sliding expiry: bumped when expires_at is extended
);
CREATE INDEX idx_session_user_id ON session (user_id);

-- 4. verification: single-use tokens for emailed links.
--    identifier says what the token is for and for whom ('email-verification:<user id>',
--    'password-reset:<user id>'). Better Auth stores the value in clear; we store the SHA-256 of
--    the emailed token, for the same reason as sessions. value is UNIQUE because the link
--    carries only the token, so we look the row up BY VALUE.
CREATE TABLE verification (
    id         TEXT PRIMARY KEY,
    identifier TEXT NOT NULL,
    value      TEXT NOT NULL UNIQUE,
    expires_at TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);
CREATE INDEX idx_verification_identifier ON verification (identifier);

-- 5. Expand/contract data move: every existing user becomes a 'credential' account carrying the
--    old password_hash, then the column is dropped (the "contract" step). The account id reuses
--    the user id: unique (there is no account row yet), and it makes the Down migration trivial.
--
--    LEGACY HASHES: 00001 stored bcrypt hashes. The service only accepts argon2id, so these
--    users' passwords no longer verify (login answers 401) until they use "forget password",
--    which sets a fresh argon2id hash. The alternative, verify-bcrypt-then-rehash-as-argon2id on
--    login, migrates silently but keeps bcrypt code and dependencies alive for as long as there
--    are dormant accounts; we chose the explicit reset. See README, "Migrating legacy hashes".
INSERT INTO account (id, user_id, provider_id, account_id, password, created_at, updated_at)
SELECT id, id, 'credential', id, password_hash, created_at, created_at FROM users;

-- SQLite >= 3.35 supports DROP COLUMN (it refuses if the column is indexed or referenced).
ALTER TABLE users DROP COLUMN password_hash;

-- +goose Down
-- Restore password_hash from the credential account. Users without one (GitHub-only) get '',
-- which can never verify. Linked-account and session data is lost: Down is for dev databases.
ALTER TABLE users ADD COLUMN password_hash TEXT NOT NULL DEFAULT '';
UPDATE users SET password_hash = COALESCE(
    (SELECT a.password FROM account a WHERE a.user_id = users.id AND a.provider_id = 'credential'),
    '');

DROP INDEX idx_verification_identifier;
DROP TABLE verification;
DROP INDEX idx_session_user_id;
DROP TABLE session;
DROP INDEX idx_account_user_id;
DROP TABLE account;

ALTER TABLE users DROP COLUMN updated_at;
ALTER TABLE users DROP COLUMN image;
ALTER TABLE users DROP COLUMN email_verified;
