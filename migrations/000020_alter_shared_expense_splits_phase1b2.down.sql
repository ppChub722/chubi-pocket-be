DROP INDEX IF EXISTS idx_splits_project_member;
DROP INDEX IF EXISTS idx_splits_source_pt;

ALTER TABLE shared_expense_splits
    DROP CONSTRAINT IF EXISTS splits_source_xor;

ALTER TABLE shared_expense_splits
    ALTER COLUMN source_transaction_id SET NOT NULL;

ALTER TABLE shared_expense_splits
    DROP COLUMN IF EXISTS project_member_id,
    DROP COLUMN IF EXISTS source_project_transaction_id;
