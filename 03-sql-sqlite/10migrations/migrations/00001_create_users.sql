-- +goose Up
-- Statements below the Up marker run when migrating forward. (Do not write the marker text in comments: goose parses it.)
CREATE TABLE users (
    id         INTEGER PRIMARY KEY,
    email      TEXT NOT NULL UNIQUE,
    name       TEXT NOT NULL,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

-- +goose Down
-- The exact inverse of Up, so the migration can be rolled back during development or an incident.
DROP TABLE users;
