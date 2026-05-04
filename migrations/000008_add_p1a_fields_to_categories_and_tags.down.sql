ALTER TABLE tags
    DROP COLUMN IF EXISTS icon;

ALTER TABLE categories
    DROP COLUMN IF EXISTS note,
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS include_in_report,
    DROP COLUMN IF EXISTS sort_order;
