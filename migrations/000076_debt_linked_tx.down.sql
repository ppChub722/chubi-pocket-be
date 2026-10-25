DROP INDEX IF EXISTS personal_debts_linked_transaction_uniq;
ALTER TABLE personal_debts DROP COLUMN IF EXISTS linked_transaction_id;
