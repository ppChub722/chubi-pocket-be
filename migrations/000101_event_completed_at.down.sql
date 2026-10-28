ALTER TABLE project_members DROP COLUMN IF EXISTS removed_at;
DROP TRIGGER IF EXISTS project_transactions_locked ON project_transactions;
DROP FUNCTION IF EXISTS project_transactions_locked_check();
ALTER TABLE projects DROP COLUMN IF EXISTS completed_at;
