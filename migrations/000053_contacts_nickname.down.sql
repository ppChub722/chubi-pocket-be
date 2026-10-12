ALTER TABLE contacts
    DROP COLUMN IF EXISTS show_nickname,
    DROP COLUMN IF EXISTS nickname;
