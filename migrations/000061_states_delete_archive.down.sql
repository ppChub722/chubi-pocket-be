DROP INDEX IF EXISTS idx_categories_name;
DROP INDEX IF EXISTS idx_categories_user;
ALTER TABLE categories ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived'));
CREATE UNIQUE INDEX idx_categories_name
    ON categories(user_id, type, parent_id, LOWER(name))
    NULLS NOT DISTINCT
    WHERE status = 'active';
CREATE INDEX idx_categories_user ON categories(user_id, status);
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_status_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_status_check CHECK (status IN ('active', 'archived', 'closed'));
DELETE FROM saving_entries WHERE account_id IS NULL;
ALTER TABLE saving_entries DROP CONSTRAINT saving_entries_account_id_fkey;
ALTER TABLE saving_entries ADD CONSTRAINT saving_entries_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE CASCADE;
ALTER TABLE saving_entries ALTER COLUMN account_id SET NOT NULL;
