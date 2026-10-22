-- 0.4.0-3 "paid by someone else": a bill of 123 where Lee paid 13 and I paid
-- 110 is ONE expense of 123 (my consumption: reports, budgets and my_share
-- count 123) with paid_by_amount = 13. The wallet moves only the cash part
-- (amount − paid_by_amount = 110; a bill they paid in full has no wallet).
-- The debt "I owe Lee 13" is a personal_debts row linked back to the
-- transaction by paid_for_transaction_id (NOT source_transaction_id, so it
-- never lowers my_share). The same link carries a partner's split mirrored
-- into my book: my expense of my share, paid in full by them.

ALTER TABLE transactions ADD COLUMN paid_by_amount DECIMAL(15,2);

ALTER TABLE transactions ADD CONSTRAINT transactions_paid_by_check CHECK (
    paid_by_amount IS NULL
    OR (paid_by_amount > 0 AND paid_by_amount <= amount AND type = 'expense'));

ALTER TABLE personal_debts
    ADD COLUMN paid_for_transaction_id UUID REFERENCES transactions(id) ON DELETE CASCADE;

CREATE UNIQUE INDEX idx_personal_debts_paid_for_tx
    ON personal_debts (paid_for_transaction_id) WHERE paid_for_transaction_id IS NOT NULL;

ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_source_kind_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_source_kind_check
    CHECK (source_kind IS NULL OR source_kind IN ('transaction', 'event', 'partner', 'paid_by'));
