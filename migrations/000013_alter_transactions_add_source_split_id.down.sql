DROP INDEX IF EXISTS idx_transactions_source_split;
ALTER TABLE transactions DROP COLUMN IF EXISTS source_split_id;
