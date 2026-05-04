-- 1b.1 ships personal-context splits only. Columns deferred to 1b.2 when the
-- projects migration lands:
--   source_project_transaction_id  (FK project_transactions; relaxes the
--                                    NOT-NULL on source_transaction_id below
--                                    to a 1-of-2 XOR check)
--   project_member_id              (FK project_members; optional decoration)
-- See design/spec/06-shared-expenses.md §1 + product/phase1b/db.md migration 14.

CREATE TABLE IF NOT EXISTS shared_expense_splits (
    id                    UUID           PRIMARY KEY,
    -- 1b.1: parent is always a personal transaction. NOT NULL replaces the
    -- 1b.2 XOR (source_transaction_id is NOT NULL XOR source_project_transaction_id).
    source_transaction_id UUID           NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    -- person_name is the durable display label. ALWAYS set; rewritten by the
    -- contacts module on absorb / link / contact-delete. See spec §2.5.
    person_name           VARCHAR(100)   NOT NULL,
    contact_id            UUID           REFERENCES contacts(id) ON DELETE SET NULL,
    split_type            VARCHAR(20)    NOT NULL DEFAULT 'fixed'
                            CHECK (split_type IN ('fixed', 'percent')),
    split_ratio           DECIMAL(5,2),
    owed_amount           DECIMAL(15,2)  NOT NULL CHECK (owed_amount > 0),
    created_at            TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id    UUID           REFERENCES users(id),
    updated_at            TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id    UUID           REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_splits_source_tx
    ON shared_expense_splits(source_transaction_id);

CREATE INDEX IF NOT EXISTS idx_splits_contact
    ON shared_expense_splits(contact_id)
    WHERE contact_id IS NOT NULL;

CREATE TRIGGER set_timestamp_shared_expense_splits
BEFORE UPDATE ON shared_expense_splits
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
