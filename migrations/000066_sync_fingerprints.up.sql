-- Fingerprints (POST /sync, X-Sync-Versions) take max(updated_at) + count per user; these let
-- Postgres answer from the index alone.
CREATE INDEX IF NOT EXISTS idx_transactions_user_updated ON transactions (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_transactions_account_updated ON transactions (account_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_personal_debts_user_updated ON personal_debts (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_transaction_tags_tag_updated ON transaction_tags (tag_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_project_transactions_project_updated ON project_transactions (project_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_recurring_items_user_updated ON recurring_items (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_saving_entries_user_updated ON saving_entries (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_categories_user_updated ON categories (user_id, updated_at);
CREATE INDEX IF NOT EXISTS idx_tags_user_updated ON tags (user_id, updated_at);
