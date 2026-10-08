-- Contract §5 (owner decisions 2026-10-07/08). Principle: the app is each
-- user's own ledger; a notification tells you something happened and
-- offers a one-tap action. Per type, the RECIPIENT chooses:
--   * muted_types — don't receive it (and then nothing auto-runs either)
--   * auto_types  — run its action as soon as it arrives
-- The sender has no switch any more.

ALTER TABLE user_notification_settings
    ADD COLUMN IF NOT EXISTS muted_types TEXT[] NOT NULL DEFAULT '{}',
    -- split_created auto = "add to my debts" — on by default, which keeps
    -- today's behaviour (the partner's mirror debt is created at once).
    ADD COLUMN IF NOT EXISTS auto_types  TEXT[] NOT NULL DEFAULT '{split_created}';

-- Carry the old per-flow flags over, then drop the ones auto_types replaces.
-- auto_resolve_own_in_projects stays: it's about rows I record myself, so
-- no notification is involved.
UPDATE user_notification_settings SET auto_types =
    ARRAY['split_created']
    || CASE WHEN auto_record_received_payment THEN ARRAY['split_paid'] ELSE '{}'::TEXT[] END;

ALTER TABLE user_notification_settings
    DROP COLUMN IF EXISTS auto_notify_linked_split_contacts,
    DROP COLUMN IF EXISTS auto_add_to_personal_debt_on_split_notification,
    DROP COLUMN IF EXISTS auto_record_received_payment;

-- Link a split debt to its mirror in the other user's book, so a payment on
-- one side can notify the other.
ALTER TABLE personal_debts
    ADD COLUMN IF NOT EXISTS counterpart_debt_id UUID
        REFERENCES personal_debts(id) ON DELETE SET NULL;

-- Best-effort backfill. Both rows of a pair were inserted in the same DB
-- transaction (identical created_at): the splitter's row carries the source
-- transaction and a contact linked to the partner; the partner's row has no
-- source transaction, the same amount and the opposite direction. A row is
-- paired at most once on either side — ambiguous candidates (the same person
-- twice on one bill with equal amounts) pair in id order.
WITH candidates AS (
    SELECT s.id AS sid, p.id AS pid,
           ROW_NUMBER() OVER (PARTITION BY s.id ORDER BY p.id) AS rs,
           ROW_NUMBER() OVER (PARTITION BY p.id ORDER BY s.id) AS rp
    FROM personal_debts s
    JOIN contacts c
      ON c.id = s.counterparty_contact_id
     AND c.user_id = s.user_id
     AND c.linked_user_id IS NOT NULL
    JOIN personal_debts p
      ON p.user_id = c.linked_user_id
     AND p.source_transaction_id IS NULL
     AND p.created_at = s.created_at
     AND p.amount = s.amount
     AND p.direction <> s.direction
    WHERE s.source_transaction_id IS NOT NULL
),
pairs AS (
    SELECT sid, pid FROM candidates WHERE rs = rp
)
UPDATE personal_debts d
SET counterpart_debt_id = CASE WHEN d.id = pairs.sid THEN pairs.pid ELSE pairs.sid END
FROM pairs
WHERE d.id IN (pairs.sid, pairs.pid);
