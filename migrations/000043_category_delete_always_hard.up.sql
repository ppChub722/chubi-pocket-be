-- Category delete is always a real delete (owner decision 2026-10-07,
-- replaces spec 05 §3.5 archive-when-used). Transactions / scheduled
-- transactions already drop to "no category" via ON DELETE SET NULL;
-- budgets require a category, so they go with it.

-- 1. Budgets follow their category (was RESTRICT → 500 on delete).
ALTER TABLE budgets DROP CONSTRAINT IF EXISTS budgets_category_id_fkey;
ALTER TABLE budgets
    ADD CONSTRAINT budgets_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES categories(id) ON DELETE CASCADE;

-- 2. Purge categories archived under the old rule. Children first move up
--    to the nearest non-archived ancestor (or root) — same as a live delete.
--    Max depth is 3, so a few passes always settle the chain.
DO $$
BEGIN
    FOR i IN 1..3 LOOP
        UPDATE categories c
        SET parent_id = p.parent_id
        FROM categories p
        WHERE c.parent_id = p.id
          AND p.status = 'archived';
    END LOOP;
END $$;

DELETE FROM categories WHERE status = 'archived';
