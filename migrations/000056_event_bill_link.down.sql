DROP INDEX IF EXISTS idx_project_transactions_linked_tx;
ALTER TABLE project_transactions DROP COLUMN IF EXISTS linked_transaction_id;
