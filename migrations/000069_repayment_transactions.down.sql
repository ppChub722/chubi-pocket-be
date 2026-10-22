-- Repayment transactions stay as plain Debt Paid / Debt Received rows; their
-- person link is dropped (they stop counting in the debt balance).
DROP INDEX IF EXISTS idx_transactions_repay_name;
DROP INDEX IF EXISTS idx_transactions_repay_contact;
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_repay_person_check;
ALTER TABLE transactions
    DROP COLUMN IF EXISTS repay_person_name,
    DROP COLUMN IF EXISTS repay_contact_id;
