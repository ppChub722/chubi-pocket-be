-- 0.4.1-1 B1: amount 0 on rows the SYSTEM writes (a sync from a linked side, a recurring
-- auto-create at 0). A typed amount stays > 0: the API (Go bindings / validation) refuses 0 unless
-- the caller is a system path. project_transactions and saving_entries stay > 0.
ALTER TABLE transactions DROP CONSTRAINT IF EXISTS transactions_amount_check;
ALTER TABLE transactions ADD CONSTRAINT transactions_amount_check CHECK (amount >= 0);
ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_amount_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_amount_check CHECK (amount >= 0);

-- 0.4.1-1 D9: either side of a cross-user pair can disconnect its copy; both copies become plain
-- debts with that person and this records when. counterpart_debt_id is cleared on both sides.
ALTER TABLE personal_debts ADD COLUMN disconnected_at TIMESTAMPTZ;

-- 0.4.1-1 B3 / D9: "X ตัดการเชื่อมหนี้ ฿300 แล้ว".
ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN ('split_created', 'split_paid',
    'split_received', 'split_changed', 'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite',
    'contact_link_request', 'account_invite', 'project_added', 'recurring_due', 'recurring_ending',
    'recurring_auto_created', 'debt_disconnected'));
