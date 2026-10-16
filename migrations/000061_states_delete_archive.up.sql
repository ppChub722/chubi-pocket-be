-- 0.3.3 delete / archive (plan: engineering/v0.3.3-delete-archive-plan.md).
-- Steps 3 / 8 / 9 of the plan were done by the 0.3.1-10 rebuilds (recurring,
-- events v2, goals v2); this migration carries what is left.

-- Step 7: categories.status is dead since 000043 (delete is always hard).
DROP INDEX IF EXISTS idx_categories_name;
DROP INDEX IF EXISTS idx_categories_user;
ALTER TABLE categories DROP COLUMN status;
CREATE UNIQUE INDEX idx_categories_name
    ON categories(user_id, type, parent_id, LOWER(name))
    NULLS NOT DISTINCT;
CREATE INDEX idx_categories_user ON categories(user_id);

-- Step 11: a wallet is active or archived ('closed' merged into 'archived').
UPDATE accounts SET status = 'archived' WHERE status = 'closed';
ALTER TABLE accounts DROP CONSTRAINT IF EXISTS accounts_status_check;
ALTER TABLE accounts ADD CONSTRAINT accounts_status_check CHECK (status IN ('active', 'archived'));

-- Step 12: a wallet can be deleted for real. Goal money kept on it stays:
-- a saving entry may have no wallet (NULL = unallocated: it counts for its
-- goal, never for a wallet).
ALTER TABLE saving_entries ALTER COLUMN account_id DROP NOT NULL;
ALTER TABLE saving_entries DROP CONSTRAINT saving_entries_account_id_fkey;
ALTER TABLE saving_entries ADD CONSTRAINT saving_entries_account_id_fkey
    FOREIGN KEY (account_id) REFERENCES accounts(id) ON DELETE SET NULL;
