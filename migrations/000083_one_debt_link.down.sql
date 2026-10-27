ALTER TABLE personal_debts
    ADD COLUMN linked_transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX personal_debts_linked_transaction_uniq
    ON personal_debts (linked_transaction_id) WHERE linked_transaction_id IS NOT NULL;

UPDATE personal_debts pd
SET linked_transaction_id = t.id
FROM transactions t
WHERE t.source_personal_debt_id = pd.id AND t.debt_connected AND pd.kind = 'debt';

UPDATE transactions
SET source_personal_debt_id = NULL, category_id = debt_prev_category_id
WHERE debt_connected;

ALTER TABLE transactions
    DROP COLUMN debt_connected,
    DROP COLUMN debt_prev_category_id;
