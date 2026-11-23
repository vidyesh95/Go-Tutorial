-- Shared schema for the SQL track (SQLite dialect, written to stay close to PostgreSQL).
--
-- Domain: a tiny task tracker.
--   users 1──* projects 1──* tasks *──* tags   (task_tags is the join table)
--   tasks.assignee_id → users (optional)
--
-- SQLite notes:
--   * Foreign keys are OFF by default per connection. Our Go code enables them with the
--     DSN pragma `_pragma=foreign_keys(1)`. In the sqlite3 shell run: PRAGMA foreign_keys = ON;
--   * INTEGER PRIMARY KEY is an alias for the rowid → auto-increments.
--     PostgreSQL equivalent: id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY.
--   * Timestamps are stored as ISO-8601 TEXT in UTC. PostgreSQL: TIMESTAMPTZ.

CREATE TABLE IF NOT EXISTS users (
    id            INTEGER PRIMARY KEY,
    email         TEXT    NOT NULL UNIQUE,
    name          TEXT    NOT NULL,
    password_hash TEXT    NOT NULL DEFAULT '',
    created_at    TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS projects (
    id         INTEGER PRIMARY KEY,
    owner_id   INTEGER NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    name       TEXT    NOT NULL,
    created_at TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now')),
    UNIQUE (owner_id, name) -- one user can't have two projects with the same name
);

CREATE TABLE IF NOT EXISTS tasks (
    id          INTEGER PRIMARY KEY,
    project_id  INTEGER NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
    assignee_id INTEGER          REFERENCES users (id) ON DELETE SET NULL, -- NULL = unassigned
    title       TEXT    NOT NULL CHECK (length(title) BETWEEN 1 AND 200),
    status      TEXT    NOT NULL DEFAULT 'todo' CHECK (status IN ('todo', 'doing', 'done')),
    priority    INTEGER NOT NULL DEFAULT 3 CHECK (priority BETWEEN 1 AND 5), -- 1 = highest
    due_date    TEXT,                                                        -- NULL = no deadline
    created_at  TEXT    NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%SZ', 'now'))
);

CREATE TABLE IF NOT EXISTS tags (
    id   INTEGER PRIMARY KEY,
    name TEXT NOT NULL UNIQUE
);

-- Many-to-many join table. Composite primary key prevents duplicate pairs.
CREATE TABLE IF NOT EXISTS task_tags (
    task_id INTEGER NOT NULL REFERENCES tasks (id) ON DELETE CASCADE,
    tag_id  INTEGER NOT NULL REFERENCES tags (id) ON DELETE CASCADE,
    PRIMARY KEY (task_id, tag_id)
);

-- Indexes match the queries we run most: "tasks in project X with status Y" and "my tasks".
-- Foreign key columns are NOT indexed automatically (in SQLite or PostgreSQL).
CREATE INDEX IF NOT EXISTS idx_tasks_project_status ON tasks (project_id, status);
CREATE INDEX IF NOT EXISTS idx_tasks_assignee ON tasks (assignee_id);
CREATE INDEX IF NOT EXISTS idx_task_tags_tag ON task_tags (tag_id);
