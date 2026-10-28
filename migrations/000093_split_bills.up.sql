-- 0.4.1-2 S1 (owner Q-B2): a split bill is a bill with no event, kept in a hidden one-bill "event":
-- projects.kind = 'split'. It never shows as an event (lists, numbers, settle, pickers filter
-- kind = 'event'); its people are its members (linked users active at once, no invite).
ALTER TABLE projects ADD COLUMN kind VARCHAR(10) NOT NULL DEFAULT 'event'
    CONSTRAINT projects_kind_check CHECK (kind IN ('event', 'split'));
DROP INDEX IF EXISTS idx_projects_owner;
CREATE INDEX idx_projects_owner ON projects (owner_user_id, kind, status);

-- 0.4.1-2 (rule a / f): "I disconnected my copy of this bill" (tx-detail disconnect, or deleting my
-- copy). The bill no longer reaches that user (no copy, no notices) until they connect again.
CREATE TABLE shared_bill_opt_outs (
    project_transaction_id UUID        NOT NULL REFERENCES project_transactions(id) ON DELETE CASCADE,
    user_id                UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    created_at             TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (project_transaction_id, user_id)
);

-- 0.4.1-2 S11: my private note on a copy of a shared bill (transaction) or on a debt of one; never
-- synced, never notified. The API accepts it only on shared rows.
ALTER TABLE transactions ADD COLUMN private_note TEXT;
ALTER TABLE personal_debts ADD COLUMN private_note TEXT;
