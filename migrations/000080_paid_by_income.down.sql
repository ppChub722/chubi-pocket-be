-- An income loses its paid_by: the debt goes and the wallet takes the whole amount.
DELETE FROM personal_debts WHERE paid_for_transaction_id IN
    (SELECT id FROM transactions WHERE type = 'income' AND paid_by_amount IS NOT NULL);
UPDATE accounts a SET balance = a.balance + s.pb
FROM (SELECT account_id, SUM(paid_by_amount) AS pb FROM transactions
      WHERE type = 'income' AND paid_by_amount IS NOT NULL AND account_id IS NOT NULL
      GROUP BY account_id) s
WHERE a.id = s.account_id;
UPDATE transactions SET paid_by_amount = NULL WHERE type = 'income';

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_paid_by_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_paid_by_check CHECK (
    paid_by_amount IS NULL
    OR (paid_by_amount > 0 AND paid_by_amount <= amount AND type = 'expense'));
