-- 1c — budgets table.
-- Spec: design/spec/08-budgets.md
-- Schema: design/database/schema.md §08 — Budgets

CREATE TABLE IF NOT EXISTS budgets (
    id                   UUID           PRIMARY KEY,
    user_id              UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    category_id          UUID           NOT NULL REFERENCES categories(id) ON DELETE RESTRICT,
    scope                VARCHAR(20)    NOT NULL
                           CHECK (scope IN ('user', 'project')),
    -- Project-scope budgets reference a project the caller owns. ON DELETE
    -- CASCADE so deleting the project tears down its budgets.
    project_id           UUID           REFERENCES projects(id) ON DELETE CASCADE,
    amount               DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    period               VARCHAR(20)    NOT NULL
                           CHECK (period IN ('weekly', 'monthly', 'yearly')),
    currency             VARCHAR(3)     NOT NULL,
    status               VARCHAR(20)    NOT NULL DEFAULT 'active'
                           CHECK (status IN ('active', 'archived')),
    icon_code            JSONB,
    created_at           TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id   UUID           REFERENCES users(id),
    updated_at           TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id   UUID           REFERENCES users(id),
    -- Schema §08 invariant: project_id set iff scope='project'.
    CONSTRAINT chk_budgets_scope_project CHECK (
        (scope = 'project') = (project_id IS NOT NULL)
    )
);

-- One active budget per (user, category, period, project_id). project_id
-- NULL is collapsed to a sentinel so the partial index treats user-scope
-- + project-scope as distinct rows. NULLS NOT DISTINCT (PG 15+) would
-- also work; the COALESCE form is explicit + deterministic across PG
-- versions in our docker-compose.
CREATE UNIQUE INDEX IF NOT EXISTS idx_budgets_user_category_period
    ON budgets(user_id, category_id, period, COALESCE(project_id, '00000000-0000-0000-0000-000000000000'::uuid))
    WHERE status = 'active';

-- Primary list query: caller's budgets filtered by status.
CREATE INDEX IF NOT EXISTS idx_budgets_user
    ON budgets(user_id, status);

-- Project-scope filter for the project view.
CREATE INDEX IF NOT EXISTS idx_budgets_project
    ON budgets(project_id)
    WHERE project_id IS NOT NULL;

CREATE TRIGGER set_timestamp_budgets
BEFORE UPDATE ON budgets
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
