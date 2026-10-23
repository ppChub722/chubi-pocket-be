-- 0.4.0-3 event book: turning "ลงบัญชีของฉัน" off on a bill unlinks my rows but keeps them in my
-- books; these remember the link so turning it on again links the same rows back.
ALTER TABLE transactions
    ADD COLUMN unlinked_project_transaction_id UUID REFERENCES project_transactions(id) ON DELETE SET NULL;
ALTER TABLE personal_debts
    ADD COLUMN unlinked_project_transaction_id UUID REFERENCES project_transactions(id) ON DELETE SET NULL;
-- The bill's linked_transaction_id while that tx is unlinked.
ALTER TABLE project_transactions
    ADD COLUMN unlinked_transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL;

CREATE INDEX idx_transactions_unlinked_pt
    ON transactions (unlinked_project_transaction_id) WHERE unlinked_project_transaction_id IS NOT NULL;
CREATE INDEX idx_personal_debts_unlinked_pt
    ON personal_debts (unlinked_project_transaction_id) WHERE unlinked_project_transaction_id IS NOT NULL;
