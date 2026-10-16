DROP INDEX IF EXISTS idx_categories_name;
DROP INDEX IF EXISTS idx_categories_user;
ALTER TABLE categories ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived'));
CREATE UNIQUE INDEX idx_categories_name
    ON categories(user_id, type, parent_id, LOWER(name))
    NULLS NOT DISTINCT
    WHERE status = 'active';
CREATE INDEX idx_categories_user ON categories(user_id, status);
