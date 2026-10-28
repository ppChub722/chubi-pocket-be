-- 0.4.1-2 (BE-2, owner 2026-10-28): deleting an event keeps its bills as standalone split bills;
-- every linked member hears it (event_deleted, info). One more type in the CHECK of 092.
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN ('split_created', 'split_paid',
    'split_received', 'split_changed', 'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite',
    'contact_link_request', 'account_invite', 'project_added', 'recurring_due', 'recurring_ending',
    'recurring_auto_created', 'debt_disconnected', 'shared_bill_created', 'shared_bill_deleted',
    'shared_bill_disconnected', 'event_completed', 'event_member_removed', 'event_deleted'));
