-- 0.4.1-2: every new notification type of the round in ONE CHECK (BE-1 S12; BE-2 V2 / V15).
--   shared_bill_created      — another member saved a shared bill (event or split) I have a part in
--   shared_bill_deleted      — a shared bill I hold a copy of was deleted
--   shared_bill_disconnected — another member disconnected their copy of a split bill
--   event_completed          — the owner completed an event I am a linked member of (V2)
--   event_member_removed     — the owner removed me from an event (V15)
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN ('split_created', 'split_paid',
    'split_received', 'split_changed', 'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite',
    'contact_link_request', 'account_invite', 'project_added', 'recurring_due', 'recurring_ending',
    'recurring_auto_created', 'debt_disconnected', 'shared_bill_created', 'shared_bill_deleted',
    'shared_bill_disconnected', 'event_completed', 'event_member_removed'));
