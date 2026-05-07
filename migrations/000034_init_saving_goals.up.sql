-- 1c — saving_goals table.
-- Spec: design/spec/09-saving-goals.md
-- Schema: design/database/schema.md §09 — Saving Goals

CREATE TABLE IF NOT EXISTS saving_goals (
    id                   UUID           PRIMARY KEY,
    user_id              UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                 VARCHAR(100)   NOT NULL,
    target_amount        DECIMAL(15,2)  NOT NULL CHECK (target_amount > 0),
    linked_account_id    UUID           NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    allocation_pct       DECIMAL(5,2)   NOT NULL CHECK (allocation_pct > 0 AND allocation_pct <= 100),
    deadline             DATE,
    icon_code            JSONB,
    status               VARCHAR(20)    NOT NULL DEFAULT 'active'
                           CHECK (status IN ('active', 'archived')),
    note                 TEXT,
    created_at           TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id   UUID           REFERENCES users(id),
    updated_at           TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id   UUID           REFERENCES users(id)
);

-- Primary list query: caller's goals filtered by status.
CREATE INDEX IF NOT EXISTS idx_saving_goals_user
    ON saving_goals(user_id, status);

-- Account allocation queries: sum-by-account checks for the
-- API-layer "≤ 100%" rule and the per-account allocations helper.
CREATE INDEX IF NOT EXISTS idx_saving_goals_account
    ON saving_goals(linked_account_id, status);

CREATE TRIGGER set_timestamp_saving_goals
BEFORE UPDATE ON saving_goals
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
