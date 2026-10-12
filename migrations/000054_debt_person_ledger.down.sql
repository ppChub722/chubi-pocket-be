-- Best effort: the person ledger cannot be folded back into debts with
-- repayments (data is disposable, owner 2026-10-13). Restores the columns and
-- constraints so 053 code can run; repayment entries stay as plain debts.

DROP INDEX IF EXISTS idx_transactions_source_personal_debt;
CREATE INDEX idx_transactions_source_personal_debt
    ON transactions (source_personal_debt_id) WHERE source_personal_debt_id IS NOT NULL;

DROP INDEX IF EXISTS idx_personal_debts_user;

ALTER TABLE personal_debts DROP CONSTRAINT personal_debts_direction_check;
ALTER TABLE personal_debts DROP CONSTRAINT personal_debts_source_kind_check;
ALTER TABLE personal_debts DROP CONSTRAINT personal_debts_amount_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_amount_check CHECK (amount >= 0);

UPDATE personal_debts SET direction = CASE direction WHEN 'paid' THEN 'owed_to_me' ELSE 'i_owe' END;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_direction_check
    CHECK (direction IN ('i_owe', 'owed_to_me'));

ALTER TABLE personal_debts
    ADD COLUMN settled_amount NUMERIC(15,2) NOT NULL DEFAULT 0,
    ADD COLUMN status VARCHAR(20) NOT NULL DEFAULT 'open';
UPDATE personal_debts SET status = 'settled', settled_amount = amount WHERE paid;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_check CHECK (settled_amount >= 0);
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_status_check
    CHECK (status IN ('open', 'settled', 'cancelled'));
CREATE INDEX idx_personal_debts_user ON personal_debts (user_id, status);

ALTER TABLE personal_debts
    DROP COLUMN date,
    DROP COLUMN paid,
    DROP COLUMN source_kind;
