-- Contract §5 (owner decisions 2026-10-07).

-- 1. Per-type mutes. Values are notification types; the API rejects the
--    action-required ones (invites, link requests).
ALTER TABLE user_notification_settings
    ADD COLUMN IF NOT EXISTS muted_types TEXT[] NOT NULL DEFAULT '{}';

-- 2. "Add to my debts automatically when someone splits a bill with me"
--    defaults ON, for new and existing users — so today's behaviour (the
--    partner's mirror debt is always created) doesn't change for anyone.
ALTER TABLE user_notification_settings
    ALTER COLUMN auto_add_to_personal_debt_on_split_notification SET DEFAULT TRUE;
UPDATE user_notification_settings
    SET auto_add_to_personal_debt_on_split_notification = TRUE;

-- 3. Link a split debt to its mirror in the other user's book, so payments
--    on one side can notify / settle the other.
ALTER TABLE personal_debts
    ADD COLUMN IF NOT EXISTS counterpart_debt_id UUID
        REFERENCES personal_debts(id) ON DELETE SET NULL;

-- Best-effort backfill. Both rows of a pair were inserted in the same DB
-- transaction (identical created_at): the splitter's row carries the
-- source transaction and a contact linked to the partner; the partner's
-- row has no source transaction, the same amount and the opposite
-- direction.
WITH pairs AS (
    SELECT DISTINCT ON (s.id) s.id AS sid, p.id AS pid
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
     AND p.counterpart_debt_id IS NULL
    WHERE s.source_transaction_id IS NOT NULL
      AND s.counterpart_debt_id IS NULL
    ORDER BY s.id, p.id
)
UPDATE personal_debts d
SET counterpart_debt_id = CASE WHEN d.id = pairs.sid THEN pairs.pid ELSE pairs.sid END
FROM pairs
WHERE d.id IN (pairs.sid, pairs.pid);
