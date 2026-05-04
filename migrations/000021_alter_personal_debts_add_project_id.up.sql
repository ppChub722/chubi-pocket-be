-- 1b.2: contextual project on personal_debts. Set when the debt's source
-- split is project-context, OR when manually entered against a project.
-- See design/spec/12-personal-debts.md §1.

ALTER TABLE personal_debts
    ADD COLUMN IF NOT EXISTS project_id UUID
        REFERENCES projects(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_personal_debts_project
    ON personal_debts(project_id)
    WHERE project_id IS NOT NULL;
