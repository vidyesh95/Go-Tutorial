-- +goose Up
-- Additive and safe: adding an index (or a nullable column, or a new table) does not break the
-- application version that is currently running. This is the "expand" half of expand/contract.
CREATE INDEX idx_projects_name ON projects (name);

-- +goose Down
DROP INDEX idx_projects_name;
