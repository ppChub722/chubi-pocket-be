-- Undo the EXPAND step (nothing was deleted by 124, so this is lossless for the old tables).
-- Not valid once migration 125 (CONTRACT) has run.
DROP TRIGGER IF EXISTS links_debt_bills ON personal_debt_bills;
DROP TRIGGER IF EXISTS links_debts ON personal_debts;
DROP TRIGGER IF EXISTS links_origin ON project_transactions;
DROP TRIGGER IF EXISTS links_transactions ON transactions;
DROP FUNCTION IF EXISTS trg_links_debt_bills();
DROP FUNCTION IF EXISTS trg_links_debts();
DROP FUNCTION IF EXISTS trg_links_origin();
DROP FUNCTION IF EXISTS trg_links_transactions();
DROP FUNCTION IF EXISTS split_bill_of(UUID);
DROP TRIGGER IF EXISTS sync_split_people_member ON project_members;
DROP TRIGGER IF EXISTS sync_split_people_opt_outs ON shared_bill_opt_outs;
DROP TRIGGER IF EXISTS sync_split_people_payers ON project_transaction_payers;
DROP TRIGGER IF EXISTS sync_split_people_pt ON project_transactions;
DROP FUNCTION IF EXISTS trg_sync_split_people_member();
DROP FUNCTION IF EXISTS trg_sync_split_people_bill_child();
DROP FUNCTION IF EXISTS trg_sync_split_people_pt();
DROP FUNCTION IF EXISTS sync_split_people(UUID);

ALTER TABLE personal_debt_bills DROP COLUMN split_person_id, DROP COLUMN split_transaction_id;
ALTER TABLE personal_debts DROP COLUMN unlinked_split_transaction_id, DROP COLUMN split_transaction_id;
ALTER TABLE transactions DROP COLUMN unlinked_split_transaction_id, DROP COLUMN split_person_id,
    DROP COLUMN split_transaction_id;
DROP TABLE split_transaction_people;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM project_transactions WHERE project_id IS NULL OR transaction_member_id IS NULL) THEN
        ALTER TABLE project_transactions ALTER COLUMN project_id SET NOT NULL,
            ALTER COLUMN transaction_member_id SET NOT NULL;
    END IF;
END $$;
