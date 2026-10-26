-- 0.4.1-0 "รับแทน": paid_by also goes on an income. Someone else received
-- part of my income for me: the income counts its whole amount, the wallet
-- moves only the cash part (amount − paid_by_amount), and the debt "they
-- owe me paid_by_amount" hangs on the transaction like the expense one.

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_paid_by_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_paid_by_check CHECK (
    paid_by_amount IS NULL
    OR (paid_by_amount > 0 AND paid_by_amount <= amount AND type IN ('expense', 'income')));
