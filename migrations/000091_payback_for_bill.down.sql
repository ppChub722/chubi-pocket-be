DROP INDEX IF EXISTS idx_project_transactions_paid_back_for;
ALTER TABLE project_transactions DROP COLUMN IF EXISTS paid_back_for_id;
