-- 033 down — revert IconMaker migration (best-effort; avatar_url is not restored).

DROP TABLE IF EXISTS user_pack_permissions;

-- Re-add old columns.
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS avatar_url TEXT;

ALTER TABLE accounts
    ADD COLUMN IF NOT EXISTS icon  VARCHAR(50),
    ADD COLUMN IF NOT EXISTS color VARCHAR(7);

ALTER TABLE categories
    ADD COLUMN IF NOT EXISTS icon  VARCHAR(50),
    ADD COLUMN IF NOT EXISTS color VARCHAR(7);

ALTER TABLE tags
    ADD COLUMN IF NOT EXISTS icon  VARCHAR(50),
    ADD COLUMN IF NOT EXISTS color VARCHAR(7);

ALTER TABLE contacts
    ADD COLUMN IF NOT EXISTS icon VARCHAR(50);

ALTER TABLE projects
    ADD COLUMN IF NOT EXISTS icon_id  TEXT,
    ADD COLUMN IF NOT EXISTS color_id TEXT;

ALTER TABLE project_transactions
    ADD COLUMN IF NOT EXISTS category_icon_id  TEXT,
    ADD COLUMN IF NOT EXISTS category_color_id TEXT;

-- Best-effort reverse data migration.
UPDATE accounts
SET icon  = icon_code->>'icon',
    color = icon_code->'bgColors'->>0
WHERE icon_code IS NOT NULL;

UPDATE categories
SET icon  = icon_code->>'icon',
    color = icon_code->'bgColors'->>0
WHERE icon_code IS NOT NULL;

UPDATE tags
SET icon  = icon_code->>'icon',
    color = icon_code->'iconColors'->>0
WHERE icon_code IS NOT NULL;

UPDATE contacts
SET icon = icon_code->>'icon'
WHERE icon_code IS NOT NULL;

UPDATE projects
SET icon_id  = icon_code->>'icon',
    color_id = icon_code->'bgColors'->>0
WHERE icon_code IS NOT NULL;

UPDATE project_transactions
SET category_icon_id  = category_icon_code->>'icon',
    category_color_id = category_icon_code->'bgColors'->>0
WHERE category_icon_code IS NOT NULL;

-- Drop new columns.
ALTER TABLE users
    DROP COLUMN IF EXISTS icon_code;

ALTER TABLE accounts
    DROP COLUMN IF EXISTS icon_code,
    DROP COLUMN IF EXISTS logo_url;

ALTER TABLE categories
    DROP COLUMN IF EXISTS icon_code;

ALTER TABLE tags
    DROP COLUMN IF EXISTS icon_code;

ALTER TABLE contacts
    DROP COLUMN IF EXISTS icon_code;

ALTER TABLE projects
    DROP COLUMN IF EXISTS icon_code;

ALTER TABLE project_transactions
    DROP COLUMN IF EXISTS category_icon_code;

ALTER TABLE project_members
    DROP COLUMN IF EXISTS icon_code;
