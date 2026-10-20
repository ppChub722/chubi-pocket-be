-- Schema only: the one-off items and the unrecorded items that 068 deleted are
-- not recreated (the old model has no way to rebuild them: its rows were made
-- ahead of time and are gone).
ALTER TABLE recurring_items
    DROP COLUMN IF EXISTS cleared_at,
    DROP COLUMN IF EXISTS auto_created_at;
ALTER TABLE recurring_rules
    DROP COLUMN IF EXISTS tracked_from,
    DROP COLUMN IF EXISTS due_notified_until,
    DROP COLUMN IF EXISTS auto_create,
    DROP COLUMN IF EXISTS auto_created_until,
    DROP COLUMN IF EXISTS paused_from;
ALTER TABLE recurring_items ALTER COLUMN rule_id DROP NOT NULL;
DELETE FROM notifications WHERE type = 'recurring_auto_created';
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN ('split_created', 'split_paid', 'split_received', 'split_changed', 'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite', 'contact_link_request', 'account_invite', 'project_added', 'recurring_due', 'recurring_ending'));
UPDATE notifications SET deep_link = '/pending' WHERE type = 'recurring_due' AND deep_link = '/recurring?focus=due';
