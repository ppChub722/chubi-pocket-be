-- Add source_personal_debt_id to transactions, replacing the dropped
-- source_split_id. Set when a transaction settles a personal_debt
-- (paying back / receiving back).

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS source_personal_debt_id UUID
        REFERENCES personal_debts(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_transactions_source_personal_debt
    ON transactions(source_personal_debt_id)
    WHERE source_personal_debt_id IS NOT NULL;

-- Drop the old at-most-one-source CHECK (referenced source_split_id which
-- no longer exists) and re-add with source_personal_debt_id.
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_source_at_most_one;

ALTER TABLE transactions
    ADD CONSTRAINT transactions_source_at_most_one CHECK (
        (source_personal_debt_id IS NOT NULL)::int +
        (source_project_transaction_id IS NOT NULL)::int <= 1
    );

-- The project_id-implies-source CHECK references the source columns;
-- since one was renamed, rebuild it too. Same one-way implication:
-- project_id NOT NULL ⇒ at least one source set.
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_project_id_implies_source;

ALTER TABLE transactions
    ADD CONSTRAINT transactions_project_id_implies_source CHECK (
        project_id IS NULL
        OR source_personal_debt_id IS NOT NULL
        OR source_project_transaction_id IS NOT NULL
    );
