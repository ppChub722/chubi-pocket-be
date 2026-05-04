-- Reverse the backfill: clear category_id from settlement transactions
-- that point to debt rows AND are using one of the new system categories.
-- Then drop the system category rows.

UPDATE transactions t
SET category_id = NULL
WHERE t.source_personal_debt_id IS NOT NULL
  AND t.category_id IN (
    SELECT id FROM categories
    WHERE is_system = TRUE
      AND system_kind IN ('DEBT_RECEIVED', 'DEBT_PAID')
  );

DELETE FROM categories
WHERE is_system = TRUE
  AND system_kind IN ('DEBT_RECEIVED', 'DEBT_PAID');
