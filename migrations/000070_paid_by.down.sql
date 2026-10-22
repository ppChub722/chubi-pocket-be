-- Paid-by debts become plain manual entries; their transactions keep the full
-- amount (the wallet's cached balance is not recomputed).
UPDATE personal_debts SET source_kind = NULL WHERE source_kind = 'paid_by';

ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_source_kind_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_source_kind_check
    CHECK (source_kind IS NULL OR source_kind IN ('transaction', 'event', 'partner'));

DROP INDEX IF EXISTS idx_personal_debts_paid_for_tx;
ALTER TABLE personal_debts DROP COLUMN IF EXISTS paid_for_transaction_id;

ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_paid_by_check;
ALTER TABLE transactions DROP COLUMN IF EXISTS paid_by_amount;
