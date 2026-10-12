-- Best effort: saving entries and saving drafts are lost; goals come back
-- without a wallet they can't have — down needs a wallet per goal, so the
-- goals are dropped.
DELETE FROM pending_transactions WHERE kind = 'saving';
ALTER TABLE pending_transactions DROP CONSTRAINT pending_kind_target;
ALTER TABLE pending_transactions ADD CONSTRAINT pending_kind_target CHECK (
    (kind = 'create' AND target_debt_id IS NULL AND target_transaction_id IS NULL) OR
    (kind = 'settle_debt' AND target_debt_id IS NOT NULL) OR
    (kind = 'update_tx' AND target_transaction_id IS NOT NULL));
ALTER TABLE pending_transactions DROP CONSTRAINT pending_transactions_kind_check;
ALTER TABLE pending_transactions ADD CONSTRAINT pending_transactions_kind_check
    CHECK (kind IN ('create', 'settle_debt', 'update_tx'));

DROP INDEX IF EXISTS idx_recurring_items_saving;
ALTER TABLE recurring_items DROP COLUMN saving_entry_id;
DELETE FROM recurring_rules WHERE type = 'saving';
ALTER TABLE recurring_rules DROP COLUMN goal_id;
ALTER TABLE recurring_rules DROP CONSTRAINT recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check
    CHECK (type IN ('expense', 'income', 'transfer'));

DROP TABLE saving_entries;

DELETE FROM saving_goals;
ALTER TABLE saving_goals DROP CONSTRAINT saving_goals_status_check;
ALTER TABLE saving_goals ADD CONSTRAINT saving_goals_status_check CHECK (status IN ('active', 'archived'));
ALTER TABLE saving_goals DROP COLUMN milestones, DROP COLUMN closed_at;
ALTER TABLE saving_goals ADD COLUMN linked_account_id UUID NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
                         ADD COLUMN allocation_pct NUMERIC(5,2) NOT NULL
                             CONSTRAINT saving_goals_allocation_pct_check CHECK (allocation_pct > 0 AND allocation_pct <= 100);
CREATE INDEX idx_saving_goals_account ON saving_goals (linked_account_id, status);
