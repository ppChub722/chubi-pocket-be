-- 0.4.1-1 D3: a REPAYMENT is a person-book row (personal_debts.kind = 'repayment'), like a debt:
-- tied to the person, its transaction optional (transactions.source_personal_debt_id, the same
-- link a debt's own transaction uses). Direction follows the sign as on a debt: paid = I paid
-- back (net +, its tx an expense), received = they paid me back (net −, an income). A person's
-- balance = Σ signed personal_debts rows; transactions never count.
-- Replaces 069's repayment transactions (transactions.repay_contact_id / repay_person_name).

ALTER TABLE personal_debts
    ADD COLUMN kind VARCHAR(20) NOT NULL DEFAULT 'debt',
    ADD CONSTRAINT personal_debts_kind_check CHECK (kind IN ('debt', 'repayment'));

-- Every repayment transaction becomes a row (same id, so links to it stay readable). An event
-- payback's bill link (source_project_transaction_id / unlinked_project_transaction_id) moves onto
-- the row.
INSERT INTO personal_debts (
    id, user_id, kind, direction, counterparty_contact_id, counterparty_person_name,
    source_project_transaction_id, project_id, source_kind, unlinked_project_transaction_id,
    amount, currency, date, paid, description, note,
    created_at, created_by_user_id, updated_at, updated_by_user_id)
SELECT t.id, t.user_id, 'repayment',
       CASE t.type WHEN 'expense' THEN 'paid' ELSE 'received' END,
       t.repay_contact_id, t.repay_person_name,
       t.source_project_transaction_id, t.project_id,
       CASE WHEN t.source_project_transaction_id IS NOT NULL
                 OR t.unlinked_project_transaction_id IS NOT NULL THEN 'event' END,
       t.unlinked_project_transaction_id,
       t.amount, COALESCE(a.currency, u.currency), t.date, FALSE, t.description, t.note,
       t.created_at, t.created_by_user_id, t.updated_at, t.updated_by_user_id
FROM transactions t
JOIN users u ON u.id = t.user_id
LEFT JOIN accounts a ON a.id = t.account_id
WHERE t.repay_person_name IS NOT NULL;

-- Owner 2026-10-27 (answer 7): a repayment with no wallet becomes a row without a transaction.
DELETE FROM transactions WHERE repay_person_name IS NOT NULL AND account_id IS NULL;

-- 069's check forbids a repayment with source_personal_debt_id: it goes first.
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_repay_person_check;

-- The rest stay as the row's transaction.
UPDATE transactions
SET source_personal_debt_id = id,
    source_project_transaction_id = NULL,
    unlinked_project_transaction_id = NULL
WHERE repay_person_name IS NOT NULL;

DROP INDEX IF EXISTS idx_transactions_repay_contact;
DROP INDEX IF EXISTS idx_transactions_repay_name;
ALTER TABLE transactions
    DROP COLUMN repay_contact_id,
    DROP COLUMN repay_person_name;
