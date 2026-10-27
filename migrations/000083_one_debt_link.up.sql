-- 0.4.1-1 D2: ONE link kind between a person-book row and a transaction:
-- transactions.source_personal_debt_id (connected + synced: amount, date, description, note are
-- one value). debt_connected = TRUE marks an EXISTING transaction connected later (it can be
-- disconnected; debt_prev_category_id is its category from before, restored then); FALSE = the
-- transaction was made from the row (can't be disconnected).
-- 076's soft link (personal_debts.linked_transaction_id, no sync) folds into it.

ALTER TABLE transactions
    ADD COLUMN debt_connected BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN debt_prev_category_id UUID REFERENCES categories(id) ON DELETE SET NULL;

-- Fold: the transaction becomes the row's transaction and takes the kind category; the row takes
-- the transaction's amount and date (description / note: the row's when set, else the tx's).
UPDATE transactions t
SET source_personal_debt_id = pd.id,
    debt_connected = TRUE,
    debt_prev_category_id = t.category_id,
    category_id = c.id,
    description = COALESCE(pd.description, t.description),
    note = COALESCE(pd.note, t.note)
FROM personal_debts pd, categories c
WHERE pd.linked_transaction_id = t.id
  AND t.source_personal_debt_id IS NULL
  AND NOT EXISTS (SELECT 1 FROM transactions o WHERE o.source_personal_debt_id = pd.id)
  AND c.user_id = t.user_id
  AND c.system_kind = CASE
        WHEN pd.kind = 'repayment' AND t.type = 'expense' THEN 'DEBT_PAID'
        WHEN pd.kind = 'repayment' THEN 'DEBT_RECEIVED'
        WHEN t.type = 'expense' THEN 'LOAN_GIVEN'
        ELSE 'LOAN_TAKEN' END;

UPDATE personal_debts pd
SET amount = t.amount, date = t.date,
    description = t.description, note = t.note
FROM transactions t
WHERE t.source_personal_debt_id = pd.id AND t.debt_connected AND t.amount > 0;

DROP INDEX IF EXISTS personal_debts_linked_transaction_uniq;
ALTER TABLE personal_debts DROP COLUMN linked_transaction_id;
