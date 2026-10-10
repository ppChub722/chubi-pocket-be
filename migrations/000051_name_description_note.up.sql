-- name / description / note standard (owner decision 2026-10-10).
--   Things  (accounts, categories, tags, contacts, budgets, saving_goals,
--            projects, scheduled_transactions): name + description + note.
--   Records (transactions, project_transactions, personal_debts, and the
--            pending draft JSON): description (what it was for) + note.
-- Limits are enforced by the API (name 100 · tags 50, description 200,
-- note 500 characters), so new columns are plain TEXT.

ALTER TABLE tags
    ADD COLUMN IF NOT EXISTS description TEXT,
    ADD COLUMN IF NOT EXISTS note        TEXT;

ALTER TABLE contacts RENAME COLUMN notes TO note;
ALTER TABLE contacts ADD COLUMN IF NOT EXISTS description TEXT;

ALTER TABLE transactions   ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE personal_debts ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE saving_goals   ADD COLUMN IF NOT EXISTS description TEXT;
ALTER TABLE projects       ADD COLUMN IF NOT EXISTS note        TEXT;
ALTER TABLE scheduled_transactions ADD COLUMN IF NOT EXISTS description TEXT;

-- budgets.description was the budget's title (FE fell back to the
-- category name). It moves to a real `name`; description starts empty.
ALTER TABLE budgets ADD COLUMN IF NOT EXISTS name VARCHAR(100);
UPDATE budgets b
SET name = COALESCE(NULLIF(LEFT(BTRIM(b.description), 100), ''), c.name)
FROM categories c
WHERE c.id = b.category_id;
ALTER TABLE budgets ALTER COLUMN name SET NOT NULL;
ALTER TABLE budgets ALTER COLUMN description TYPE TEXT;
UPDATE budgets SET description = NULL;
