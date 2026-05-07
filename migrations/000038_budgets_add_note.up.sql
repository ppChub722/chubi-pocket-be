-- 1c follow-up — budgets gain a free-form `note` (in addition to the
-- short `description` from migration 000037). Mirrors the description +
-- note split on accounts: description is the primary label, note is
-- longer narrative shown on the detail page only.

ALTER TABLE budgets ADD COLUMN IF NOT EXISTS note TEXT;
