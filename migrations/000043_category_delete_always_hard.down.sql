-- Purged archived categories can't be brought back; only the FK reverts.
ALTER TABLE budgets DROP CONSTRAINT IF EXISTS budgets_category_id_fkey;
ALTER TABLE budgets
    ADD CONSTRAINT budgets_category_id_fkey
    FOREIGN KEY (category_id) REFERENCES categories(id) ON DELETE RESTRICT;
