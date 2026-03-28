-- Align transactions table with design spec
-- Rename description -> note, transaction_date -> date
ALTER TABLE transactions RENAME COLUMN description TO note;
ALTER TABLE transactions RENAME COLUMN transaction_date TO date;

-- Change date column from TIMESTAMP to DATE type
ALTER TABLE transactions ALTER COLUMN date TYPE DATE USING date::DATE;

-- Add new foreign key columns
ALTER TABLE transactions ADD COLUMN transfer_to_account_id BIGINT REFERENCES accounts(id) ON DELETE SET NULL;
ALTER TABLE transactions ADD COLUMN project_id BIGINT; -- FK added after projects table created
ALTER TABLE transactions ADD COLUMN recurring_id BIGINT; -- FK added after recurrings table created
ALTER TABLE transactions ADD COLUMN photo_url TEXT;

-- Drop old linked_transaction_id (spec uses transfer_to_account_id instead)
ALTER TABLE transactions DROP COLUMN IF EXISTS linked_transaction_id;

-- Update index to use new column name
DROP INDEX IF EXISTS idx_transactions_user_date;
CREATE INDEX idx_transactions_user_date ON transactions(user_id, date);
