-- 1b.2 — The canonical project ledger. No personal account is touched at
-- insert time; real money moves via claim (actor mirrors onto personal book)
-- or via split-resolve (debtors/creditors create personal entries).
-- See design/spec/10-projects.md §1.

CREATE TABLE IF NOT EXISTS project_transactions (
    id                      UUID           PRIMARY KEY,
    project_id              UUID           NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
    -- Whose money moved (actor). May be linked, contact, or ad-hoc.
    transaction_member_id   UUID           NOT NULL REFERENCES project_members(id),
    -- The linked user who recorded this row. Owns edit/delete rights
    -- (owner override applies).
    record_user_id          UUID           NOT NULL REFERENCES users(id),
    type                    VARCHAR(20)    NOT NULL
                              CHECK (type IN ('expense', 'income')),
    amount                  DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    currency                VARCHAR(3)     NOT NULL,
    date                    DATE           NOT NULL,
    -- 1b: nullable; project-scoped categories deferred to Phase 2+.
    category_id             UUID           REFERENCES categories(id) ON DELETE SET NULL,
    note                    TEXT,
    created_at              TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id      UUID           REFERENCES users(id),
    updated_at              TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id      UUID           REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_project_tx_project
    ON project_transactions(project_id, date DESC);

CREATE INDEX IF NOT EXISTS idx_project_tx_member
    ON project_transactions(transaction_member_id);

CREATE INDEX IF NOT EXISTS idx_project_tx_recorder
    ON project_transactions(record_user_id);

CREATE TRIGGER set_timestamp_project_transactions
BEFORE UPDATE ON project_transactions
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
