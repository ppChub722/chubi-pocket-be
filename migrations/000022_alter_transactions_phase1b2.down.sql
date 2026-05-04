DROP INDEX IF EXISTS idx_transactions_source_pt;
DROP INDEX IF EXISTS idx_transactions_project;

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_project_id_iff_source,
    DROP CONSTRAINT IF EXISTS transactions_source_at_most_one;

ALTER TABLE transactions
    DROP COLUMN IF EXISTS source_project_transaction_id,
    DROP COLUMN IF EXISTS project_id;
