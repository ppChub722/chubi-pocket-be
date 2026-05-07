-- 1c follow-up — budgets icon comes from the linked category, not from
-- a per-budget icon picker. Drop the unused icon_code column and add a
-- description that the FE renders as the budget's primary label
-- (falling back to the category name when description is null).

ALTER TABLE budgets DROP COLUMN IF EXISTS icon_code;
ALTER TABLE budgets ADD COLUMN IF NOT EXISTS description VARCHAR(200);
