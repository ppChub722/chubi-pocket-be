DROP INDEX IF EXISTS idx_contacts_last_used;

ALTER TABLE contacts
    DROP COLUMN IF EXISTS last_used_at;
