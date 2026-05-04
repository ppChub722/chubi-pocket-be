-- Reverse 000027_projects_redesign.up.sql.

ALTER TABLE project_members
    ADD COLUMN contact_id UUID REFERENCES contacts(id) ON DELETE SET NULL;

ALTER TABLE project_transactions
    ADD COLUMN category_id UUID REFERENCES categories(id) ON DELETE SET NULL;

DROP INDEX IF EXISTS project_transactions_parent_idx;

ALTER TABLE project_transactions
    DROP COLUMN marks,
    DROP COLUMN parent_project_transaction_id;
