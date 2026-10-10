-- Editable splits (owner 2026-10-10): a bill's splits can be changed after
-- save (PUT /v1/transactions/:id/splits).
--
-- 1) Repayments don't constrain edits: a debt's amount may drop below what
--    was already repaid. outstanding = amount − settled_amount can then go
--    negative = overpaid, shown as owed the other way. A removed person who
--    already repaid keeps the row at amount 0 (overpaid by what they paid).
ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_amount_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_amount_check CHECK (amount >= 0);
ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_check;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_check CHECK (settled_amount >= 0);

-- 2) split_changed: a split the recipient already added to their book was
--    re-amounted or removed. One-shot "อัปเดตตาม" updates their own debt.
ALTER TABLE notifications DROP CONSTRAINT IF EXISTS notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check
    CHECK (type IN (
        'split_created',
        'split_paid',
        'split_received',
        'split_changed',
        'project_tx_recorded_for_you',
        'project_tx_changed',
        'project_invite',
        'contact_link_request',
        'account_invite',
        'project_added'
    ));
