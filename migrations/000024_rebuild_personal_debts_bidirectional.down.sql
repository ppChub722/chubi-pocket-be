-- Reverting this migration is destructive (data loss): the new
-- personal_debts shape can't round-trip back to the old splits + debts
-- two-table layout. Down restores the old empty structure only.
DROP TRIGGER IF EXISTS set_timestamp_personal_debts ON personal_debts;
DROP TABLE IF EXISTS personal_debts;

-- Restore the old empty splits table.
CREATE TABLE IF NOT EXISTS shared_expense_splits (
    id                    UUID           PRIMARY KEY,
    source_transaction_id UUID           NOT NULL REFERENCES transactions(id) ON DELETE CASCADE,
    person_name           VARCHAR(100)   NOT NULL,
    contact_id            UUID           REFERENCES contacts(id) ON DELETE SET NULL,
    split_type            VARCHAR(20)    NOT NULL DEFAULT 'fixed',
    split_ratio           DECIMAL(5,2),
    owed_amount           DECIMAL(15,2)  NOT NULL CHECK (owed_amount > 0),
    created_at            TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id    UUID           REFERENCES users(id),
    updated_at            TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id    UUID           REFERENCES users(id)
);

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS source_split_id UUID
        REFERENCES shared_expense_splits(id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS personal_debts (
    id  UUID PRIMARY KEY
);
