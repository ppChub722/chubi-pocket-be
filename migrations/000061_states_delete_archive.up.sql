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
