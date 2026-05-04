-- Phase 1b.1: a personal transactions row created via /shared-expenses/splits/
-- :id/{pay,receive} or /personal-debts/:id/pay points back at the originating
-- split via source_split_id. The CHECK and project_id column are added in 1b.2
-- alongside source_project_transaction_id (claim flow).

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS source_split_id UUID
        REFERENCES shared_expense_splits(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_transactions_source_split
    ON transactions(source_split_id)
    WHERE source_split_id IS NOT NULL;
