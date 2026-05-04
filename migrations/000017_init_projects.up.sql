-- 1b.2 — Projects. Thin container for shared / multi-actor expense tracking.
-- See design/spec/10-projects.md §1, §3.1 (lifecycle).

CREATE TABLE IF NOT EXISTS projects (
    id                  UUID           PRIMARY KEY,
    owner_user_id       UUID           NOT NULL REFERENCES users(id),
    name                VARCHAR(100)   NOT NULL,
    type                VARCHAR(30),
    description         TEXT,
    start_date          DATE,
    end_date            DATE,
    status              VARCHAR(20)    NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'completed', 'cancelled', 'archived')),
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_projects_owner
    ON projects(owner_user_id, status);

CREATE INDEX IF NOT EXISTS idx_projects_status
    ON projects(status, updated_at DESC);

CREATE TRIGGER set_timestamp_projects
BEFORE UPDATE ON projects
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
