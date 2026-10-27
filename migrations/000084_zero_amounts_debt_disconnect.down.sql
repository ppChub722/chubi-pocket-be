DELETE FROM notifications WHERE type = 'debt_disconnected';
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN ('split_created', 'split_paid',
    'split_received', 'split_changed', 'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite',
    'contact_link_request', 'account_invite', 'project_added', 'recurring_due', 'recurring_ending',
    'recurring_auto_created'));

ALTER TABLE personal_debts DROP COLUMN disconnected_at;

DELETE FROM personal_debts WHERE amount = 0;
DELETE FROM transactions WHERE amount = 0;
ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_amount_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_amount_check CHECK (amount > 0);
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_amount_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_amount_check CHECK (amount > 0);
