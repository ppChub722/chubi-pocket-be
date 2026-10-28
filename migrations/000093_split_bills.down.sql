ALTER TABLE personal_debts DROP COLUMN private_note;
ALTER TABLE transactions DROP COLUMN private_note;
DROP TABLE shared_bill_opt_outs;
DELETE FROM projects WHERE kind = 'split';
DROP INDEX IF EXISTS idx_projects_owner;
CREATE INDEX idx_projects_owner ON projects (owner_user_id, status);
ALTER TABLE projects DROP COLUMN kind;
