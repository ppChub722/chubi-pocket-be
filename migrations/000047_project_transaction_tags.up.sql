-- Project rows: free tags replace the per-row category (owner design
-- 2026-10-09). Tags are plain names on the row — the project's tag list is
-- whatever its rows use, so an unused tag simply disappears. The old
-- category name becomes the row's first tag; the icon is dropped.
ALTER TABLE project_transactions
    ADD COLUMN IF NOT EXISTS tags TEXT[] NOT NULL DEFAULT '{}';

UPDATE project_transactions
SET tags = ARRAY[btrim(category_name)]
WHERE category_name IS NOT NULL AND btrim(category_name) <> '';

ALTER TABLE project_transactions
    DROP COLUMN IF EXISTS category_name,
    DROP COLUMN IF EXISTS category_icon_code;
