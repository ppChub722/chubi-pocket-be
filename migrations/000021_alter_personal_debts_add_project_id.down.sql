DROP INDEX IF EXISTS idx_personal_debts_project;
ALTER TABLE personal_debts DROP COLUMN IF EXISTS project_id;
