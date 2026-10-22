-- 0.4.0-3 debt model: a REPAYMENT is a transaction tied to the PERSON, not a
-- personal_debts row. "จ่ายคืนลี 400" = an expense of 400 linked to ลี;
-- "รับคืนจาก chalee 82" = an income linked to chalee. The person columns
-- mirror personal_debts.counterparty_*: a contact (optional, SET NULL on
-- contact delete) + the typed-name snapshot (always set on a repayment).
-- Balance per person = Σ signed debts + Σ signed repayments (expense +,
-- income −). A debt's own linked transaction (source_personal_debt_id) never
-- counts. Old repayment-like debt rows are NOT converted (they can't be told
-- apart from real debts).

ALTER TABLE transactions
    ADD COLUMN repay_contact_id UUID REFERENCES contacts(id) ON DELETE SET NULL,
    ADD COLUMN repay_person_name VARCHAR(100);

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
