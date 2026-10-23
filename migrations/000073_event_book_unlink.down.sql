DROP INDEX IF EXISTS idx_personal_debts_unlinked_pt;
DROP INDEX IF EXISTS idx_transactions_unlinked_pt;
ALTER TABLE project_transactions DROP COLUMN IF EXISTS unlinked_transaction_id;
ALTER TABLE personal_debts DROP COLUMN IF EXISTS unlinked_project_transaction_id;
ALTER TABLE transactions DROP COLUMN IF EXISTS unlinked_project_transaction_id;
