DROP INDEX IF EXISTS idx_transactions_scheduled;
ALTER TABLE transactions DROP COLUMN IF EXISTS scheduled_transaction_id;

DROP TRIGGER IF EXISTS set_timestamp_scheduled_transactions ON scheduled_transactions;
DROP TABLE IF EXISTS scheduled_transactions;
