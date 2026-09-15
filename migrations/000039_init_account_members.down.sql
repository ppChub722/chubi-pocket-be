-- Restore the pre-039 notification type CHECK (removes 'account_invite').
-- Any account_invite rows must go first or the narrowed CHECK fails.
DELETE FROM notifications WHERE type = 'account_invite';
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check
    CHECK (type IN (
        'split_created',
        'split_paid',
        'split_received',
        'project_tx_recorded_for_you',
        'project_tx_changed',
        'project_invite',
        'contact_link_request'
    ));

DROP TRIGGER IF EXISTS set_timestamp_account_members ON account_members;
DROP TABLE IF EXISTS account_members;
