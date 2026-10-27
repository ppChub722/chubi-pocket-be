-- Best effort: repayment rows WITH a transaction go back to 069's repayment transactions;
-- ledger-only repayment rows are dropped (they had no transaction to go back to).

ALTER TABLE transactions
    ADD COLUMN repay_contact_id UUID REFERENCES contacts(id) ON DELETE SET NULL,
    ADD COLUMN repay_person_name VARCHAR(100);

UPDATE transactions t
SET repay_contact_id = pd.counterparty_contact_id,
    repay_person_name = pd.counterparty_person_name,
    source_personal_debt_id = NULL,
    source_project_transaction_id = pd.source_project_transaction_id,
    unlinked_project_transaction_id = pd.unlinked_project_transaction_id
FROM personal_debts pd
WHERE pd.kind = 'repayment' AND t.source_personal_debt_id = pd.id;

DELETE FROM personal_debts WHERE kind = 'repayment';

ALTER TABLE transactions ADD CONSTRAINT transactions_repay_person_check CHECK (
    (repay_contact_id IS NULL AND repay_person_name IS NULL)
    OR (repay_person_name IS NOT NULL AND btrim(repay_person_name) <> ''
        AND type IN ('expense', 'income')
        AND source_personal_debt_id IS NULL
        AND transfer_group_id IS NULL));
CREATE INDEX idx_transactions_repay_contact
    ON transactions (user_id, repay_contact_id) WHERE repay_contact_id IS NOT NULL;
CREATE INDEX idx_transactions_repay_name
    ON transactions (user_id, LOWER(TRIM(repay_person_name)))
    WHERE repay_person_name IS NOT NULL AND repay_contact_id IS NULL;

ALTER TABLE personal_debts
    DROP CONSTRAINT IF EXISTS personal_debts_kind_check,
    DROP COLUMN kind;
