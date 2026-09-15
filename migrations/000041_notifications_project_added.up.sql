-- Quick create from bills (spec §10/4.24): auto-added members get an
-- informational 'project_added' notification — consent is implied by the
-- underlying splits / shared wallet, so there is no accept/reject action.
-- Same CHECK-swap pattern as migration 000039's 'account_invite'.

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
        'account_invite',
        'project_added'
    ));
