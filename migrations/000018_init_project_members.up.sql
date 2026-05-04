-- 1b.2 — Project members. Three kinds share this table:
--   linked   (user_id IS NOT NULL)
--   contact  (contact_id IS NOT NULL, user_id IS NULL)
--   ad-hoc   (both NULL)
-- See design/spec/10-projects.md §1.

CREATE TABLE IF NOT EXISTS project_members (
    id                  UUID           PRIMARY KEY,
    project_id          UUID           NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    user_id             UUID           REFERENCES users(id) ON DELETE SET NULL,
    contact_id          UUID           REFERENCES contacts(id) ON DELETE SET NULL,
    display_name        VARCHAR(100)   NOT NULL,
    role                VARCHAR(20)    NOT NULL DEFAULT 'contributor'
                          CHECK (role IN ('owner', 'contributor', 'viewer')),
    status              VARCHAR(20)    NOT NULL DEFAULT 'pending'
                          CHECK (status IN ('pending', 'active', 'left')),
    invited_at          TIMESTAMPTZ,
    joined_at           TIMESTAMPTZ,
    left_at             TIMESTAMPTZ,
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id)
);

-- A user can be at most one member per project.
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_members_user
    ON project_members(project_id, user_id)
    WHERE user_id IS NOT NULL;

-- Active-member queries.
CREATE INDEX IF NOT EXISTS idx_project_members_project
    ON project_members(project_id, status);

-- "What projects does this user belong to" reverse lookup.
CREATE INDEX IF NOT EXISTS idx_project_members_user_reverse
    ON project_members(user_id, status)
    WHERE user_id IS NOT NULL;

-- Exactly one owner per project (enforced by partial unique index on role).
CREATE UNIQUE INDEX IF NOT EXISTS idx_project_members_owner
    ON project_members(project_id)
    WHERE role = 'owner';

CREATE TRIGGER set_timestamp_project_members
BEFORE UPDATE ON project_members
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
