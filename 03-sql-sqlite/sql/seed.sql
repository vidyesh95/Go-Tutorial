-- Sample data. Run after schema.sql. Explicit ids keep examples predictable.

INSERT INTO users (id, email, name) VALUES
    (1, 'asha@example.com', 'Asha'),
    (2, 'ben@example.com',  'Ben'),
    (3, 'chen@example.com', 'Chen'),
    (4, 'dara@example.com', 'Dara');   -- Dara owns nothing and has no tasks (useful for LEFT JOIN demos)

INSERT INTO projects (id, owner_id, name) VALUES
    (1, 1, 'Website Redesign'),
    (2, 1, 'Mobile App'),
    (3, 2, 'Data Pipeline'),
    (4, 3, 'Hiring');                  -- project with no tasks

INSERT INTO tasks (id, project_id, assignee_id, title, status, priority, due_date) VALUES
    (1,  1, 1,    'Pick color palette',        'done',  3, '2026-10-01'),
    (2,  1, 2,    'Build landing page',        'doing', 2, '2026-12-01'),
    (3,  1, NULL, 'Write copy for about page', 'todo',  4, NULL),
    (4,  1, 3,    'Set up analytics',          'todo',  3, '2026-12-15'),
    (5,  2, 1,    'Login screen',              'done',  1, '2026-09-20'),
    (6,  2, 1,    'Push notifications',        'doing', 2, '2026-11-30'),
    (7,  2, 2,    'Offline mode',              'todo',  2, '2027-01-15'),
    (8,  2, NULL, 'App store screenshots',     'todo',  5, NULL),
    (9,  3, 2,    'Ingest CSV files',          'done',  2, '2026-08-01'),
    (10, 3, 2,    'Deduplicate records',       'doing', 1, '2026-11-25'),
    (11, 3, 3,    'Nightly backfill job',      'todo',  3, '2026-12-31'),
    (12, 3, NULL, 'Alerting on failures',      'todo',  1, NULL),
    (13, 1, 2,    'Accessibility audit',       'todo',  2, '2026-12-10'),
    (14, 2, 3,    'Dark mode',                 'done',  4, '2026-10-30'),
    (15, 3, 1,    'Document schema',           'todo',  4, NULL);

INSERT INTO tags (id, name) VALUES
    (1, 'frontend'), (2, 'backend'), (3, 'urgent'), (4, 'design'), (5, 'ops');

INSERT INTO task_tags (task_id, tag_id) VALUES
    (1, 4), (2, 1), (2, 3), (3, 4), (4, 2), (5, 1), (6, 2), (6, 3),
    (7, 1), (7, 2), (9, 2), (10, 2), (10, 3), (11, 5), (12, 5), (12, 3),
    (13, 1), (14, 1), (14, 4);
