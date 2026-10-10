DELETE FROM notifications WHERE type = 'split_changed';
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

-- Overpaid / zero-amount rows can't satisfy the old checks; cap them first.
UPDATE personal_debts SET settled_amount = amount WHERE settled_amount > amount;
DELETE FROM personal_debts WHERE amount = 0;
ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_check
    CHECK (settled_amount >= 0 AND settled_amount <= amount);
ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_amount_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_amount_check CHECK (amount > 0);
