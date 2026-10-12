-- 0.3.1-10 debts rework: personal_debts becomes a person ledger.
-- An entry = {person, direction paid | received, amount > 0, date, description,
-- note, paid label, optional wallet transaction, optional split source}.
-- net per person = Σ paid − Σ received. No cancel, no settled_amount / status.
-- The wallet side of an entry is transactions.source_personal_debt_id (one
-- transaction per entry: one record).

ALTER TABLE personal_debts DROP CONSTRAINT personal_debts_direction_check;
ALTER TABLE personal_debts
    ADD COLUMN date DATE,
    ADD COLUMN paid BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN source_kind VARCHAR(20),
    ADD COLUMN migrated_from_tx UUID;

-- Direction: owed_to_me (they owe me) = I paid; i_owe = they paid / I received.
UPDATE personal_debts SET direction = CASE direction WHEN 'owed_to_me' THEN 'paid' ELSE 'received' END;

-- Where a split-made entry came from (a manual entry stays NULL).
UPDATE personal_debts SET source_kind = 'transaction' WHERE source_transaction_id IS NOT NULL;
UPDATE personal_debts SET source_kind = 'partner'
    WHERE source_kind IS NULL AND counterpart_debt_id IS NOT NULL;

UPDATE personal_debts d SET date = COALESCE(
    (SELECT t.date FROM transactions t WHERE t.id = d.source_transaction_id),
    (d.created_at AT TIME ZONE 'Asia/Bangkok')::date);

-- "Settled" becomes the paid label (it never changes numbers now).
UPDATE personal_debts SET paid = TRUE WHERE status = 'settled';

-- Every repayment transaction becomes its own entry with the same person, and
-- the transaction is that entry's wallet side.
INSERT INTO personal_debts
    (id, user_id, direction, counterparty_contact_id, counterparty_person_name,
     amount, settled_amount, currency, status, description, note, date, paid,
     created_at, created_by_user_id, updated_at, updated_by_user_id, migrated_from_tx)
SELECT gen_random_uuid(), t.user_id,
       CASE t.type WHEN 'expense' THEN 'paid' ELSE 'received' END,
       d.counterparty_contact_id, d.counterparty_person_name,
       t.amount, 0, d.currency, 'open', t.description, t.note, t.date, FALSE,
       t.created_at, t.user_id, t.updated_at, t.user_id, t.id
FROM transactions t
JOIN personal_debts d ON d.id = t.source_personal_debt_id
WHERE t.type IN ('expense', 'income');

UPDATE transactions t SET source_personal_debt_id = d.id
FROM personal_debts d WHERE d.migrated_from_tx = t.id;

-- Stop counting = delete (no cancel any more).
DELETE FROM personal_debts WHERE status = 'cancelled';
-- A split removed after a repayment was kept at amount 0; its repayment is an
-- entry of its own now.
DELETE FROM personal_debts WHERE amount <= 0;

ALTER TABLE personal_debts
    DROP CONSTRAINT personal_debts_check,
    DROP CONSTRAINT personal_debts_status_check,
    DROP COLUMN settled_amount,
    DROP COLUMN status,
    DROP COLUMN migrated_from_tx;
ALTER TABLE personal_debts ALTER COLUMN date SET NOT NULL;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_direction_check
    CHECK (direction IN ('paid', 'received'));
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_source_kind_check
    CHECK (source_kind IS NULL OR source_kind IN ('transaction', 'event', 'partner'));
ALTER TABLE personal_debts DROP CONSTRAINT personal_debts_amount_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_amount_check CHECK (amount > 0);

DROP INDEX IF EXISTS idx_personal_debts_user;
CREATE INDEX idx_personal_debts_user ON personal_debts (user_id, date DESC, created_at DESC);

-- One wallet transaction per entry.
DROP INDEX IF EXISTS idx_transactions_source_personal_debt;
CREATE UNIQUE INDEX idx_transactions_source_personal_debt
    ON transactions (source_personal_debt_id) WHERE source_personal_debt_id IS NOT NULL;

-- split_paid is retired (no notifications on pay / receive).
DELETE FROM notifications WHERE type = 'split_paid';
UPDATE user_notification_settings
    SET muted_types = array_remove(muted_types, 'split_paid'),
        auto_types = array_remove(auto_types, 'split_paid');
