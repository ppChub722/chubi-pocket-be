ALTER TABLE project_transactions
    ADD COLUMN IF NOT EXISTS category_name      TEXT,
    ADD COLUMN IF NOT EXISTS category_icon_code JSONB;

UPDATE project_transactions
SET category_name = tags[1]
WHERE cardinality(tags) > 0;

ALTER TABLE project_transactions DROP COLUMN IF EXISTS tags;
