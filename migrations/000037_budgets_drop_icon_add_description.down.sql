-- Reverse 000037: re-add icon_code, drop description.

ALTER TABLE budgets DROP COLUMN IF EXISTS description;
ALTER TABLE budgets ADD COLUMN IF NOT EXISTS icon_code JSONB;
