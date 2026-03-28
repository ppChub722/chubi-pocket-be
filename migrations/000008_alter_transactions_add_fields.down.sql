DROP INDEX IF EXISTS idx_transactions_user_date;

ALTER TABLE transactions DROP COLUMN IF EXISTS photo_url;
ALTER TABLE transactions DROP COLUMN IF EXISTS recurring_id;
ALTER TABLE transactions DROP COLUMN IF EXISTS project_id;
ALTER TABLE transactions DROP COLUMN IF EXISTS transfer_to_account_id;

ALTER TABLE transactions ALTER COLUMN date TYPE TIMESTAMP WITH TIME ZONE USING date::TIMESTAMP WITH TIME ZONE;

ALTER TABLE transactions RENAME COLUMN date TO transaction_date;
ALTER TABLE transactions RENAME COLUMN note TO description;

ALTER TABLE transactions ADD COLUMN IF NOT EXISTS linked_transaction_id BIGINT REFERENCES transactions(id) ON DELETE SET NULL;

CREATE INDEX idx_transactions_user_date ON transactions(user_id, transaction_date);
