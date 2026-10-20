-- 0.4.0-2: recurring on the "computed occurrences" model.
-- Upcoming occurrences are no longer stored: the BE computes them from the
-- rule. A row exists only when an occurrence is recorded (a tx / paid) or the
-- user touched it (edited / skipped). One-off upcoming items go: a future-dated
-- transaction is just a transaction.
-- (Dates below use the DB's time zone; the app's "today" is the user's, so a
-- backfilled day can be off by one near midnight.)

-- 1. One-off items (no rule) and their pending drafts are deleted. A one-off
--    that already became a transaction keeps the transaction.
DELETE FROM pending_transactions
WHERE id IN (SELECT pending_id FROM recurring_items WHERE rule_id IS NULL AND pending_id IS NOT NULL);
DELETE FROM recurring_items WHERE rule_id IS NULL;

-- 2. Every item belongs to a rule from now on.
ALTER TABLE recurring_items ALTER COLUMN rule_id SET NOT NULL;

-- 3. Unrecorded, untouched rows become computed: delete them (a due one comes
--    back as a computed `due` occurrence; legacy rows with a live pending draft
--    or a transaction stay). Done BEFORE tracked_from is worked out, so that
--    only the rows that stay count.
DELETE FROM recurring_items
WHERE transaction_id IS NULL AND saving_entry_id IS NULL AND pending_id IS NULL
  AND NOT edited AND NOT manual AND NOT archived;

-- 4. Rule columns:
--    tracked_from        occurrences before it are history (paid implicitly), never overdue;
--    due_notified_until  the due notice has counted the occurrences up to this day;
--    auto_create         record the tx by itself when an occurrence comes due;
--    auto_created_until  auto-create has handled the occurrences up to this day;
--    paused_from         the day it was paused: resuming skips the gap.
ALTER TABLE recurring_rules
    ADD COLUMN tracked_from        DATE,
    ADD COLUMN due_notified_until  DATE,
    ADD COLUMN auto_create         BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN auto_created_until  DATE,
    ADD COLUMN paused_from         DATE;
UPDATE recurring_rules r SET
    tracked_from = LEAST(r.created_at::date,
                         COALESCE((SELECT MIN(i.slot) FROM recurring_items i WHERE i.rule_id = r.id), r.created_at::date)),
    due_notified_until = CURRENT_DATE,
    paused_from = CASE WHEN r.status <> 'active' THEN r.updated_at::date END;
ALTER TABLE recurring_rules
    ALTER COLUMN tracked_from SET NOT NULL,
    ALTER COLUMN tracked_from SET DEFAULT CURRENT_DATE;

-- 5. Item columns: cleared_at = paid without a transaction ("แม่จ่ายให้");
--    auto_created_at = the transaction was recorded by auto-create.
ALTER TABLE recurring_items
    ADD COLUMN cleared_at       TIMESTAMPTZ,
    ADD COLUMN auto_created_at  TIMESTAMPTZ;

-- 6. Notifications: the auto-create notice type, and the due notice now opens
--    the recurring card (it used to open the pending page).
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN ('split_created', 'split_paid', 'split_received', 'split_changed', 'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite', 'contact_link_request', 'account_invite', 'project_added', 'recurring_due', 'recurring_ending', 'recurring_auto_created'));
UPDATE notifications SET deep_link = '/recurring?focus=due' WHERE type = 'recurring_due' AND deep_link = '/pending';
