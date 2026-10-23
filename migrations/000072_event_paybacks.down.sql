DELETE FROM project_transactions WHERE type = 'payback';
ALTER TABLE project_transactions DROP CONSTRAINT IF EXISTS project_transactions_type_check;
ALTER TABLE project_transactions ADD CONSTRAINT project_transactions_type_check
    CHECK (type IN ('expense', 'income'));
