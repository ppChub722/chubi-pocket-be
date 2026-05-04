ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_project_id_implies_source;
ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_source_at_most_one;

DROP INDEX IF EXISTS idx_transactions_source_personal_debt;

ALTER TABLE transactions DROP COLUMN IF EXISTS source_personal_debt_id;
