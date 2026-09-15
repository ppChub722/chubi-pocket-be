-- Restore the pre-041 notification type CHECK (removes 'project_added').
-- Any project_added rows must go first or the narrowed CHECK fails.
DELETE FROM notifications WHERE type = 'project_added';
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check
    CHECK (type IN (
        'split_created',
        'split_paid',
        'split_received',
        'project_tx_recorded_for_you',
        'project_tx_changed',
        'project_invite',
        'contact_link_request',
        'account_invite'
    ));
