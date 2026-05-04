-- Phase 1a additions for the Manage Categories / Manage Tags screens.
-- Spec: design/spec/05-categories-tags.md §1.1.

ALTER TABLE categories
    ADD COLUMN IF NOT EXISTS sort_order        INT     NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS include_in_report BOOLEAN NOT NULL DEFAULT TRUE,
    ADD COLUMN IF NOT EXISTS description       TEXT,
    ADD COLUMN IF NOT EXISTS note              TEXT;

-- Backfill sort_order so existing rows have a deterministic ordering within
-- their sibling group. row_number() ordered by id (UUID v7 → time-ordered)
-- approximates "creation order".
UPDATE categories AS c
SET sort_order = sub.rn - 1
FROM (
    SELECT id,
           ROW_NUMBER() OVER (
               PARTITION BY user_id, type, parent_id
               ORDER BY id
           ) AS rn
    FROM categories
) AS sub
WHERE c.id = sub.id
  AND c.sort_order = 0;

-- System categories representing money movement, not spending — exclude from
-- reports by default. Adjustment ± and Transfer In/Out should never inflate a
-- spending total.
UPDATE categories
SET include_in_report = FALSE
WHERE is_system = TRUE
  AND system_kind IN ('ADJUST_IN', 'ADJUST_OUT', 'TRANSFER_IN', 'TRANSFER_OUT');

ALTER TABLE tags
    ADD COLUMN IF NOT EXISTS icon VARCHAR(50);
