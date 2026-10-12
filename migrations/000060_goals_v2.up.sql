-- Saving goals v2 (0.3.1-10, owner mockup goals-v2.html): savings are a
-- NOTEBOOK. A saving entry moves an amount of ONE wallet between buckets
-- (the wallet's available money, the spare bucket, a goal); never a real
-- transaction. Goals lose their wallet / % (old goals stay, with 0 saved).

DROP INDEX IF EXISTS idx_saving_goals_account;
ALTER TABLE saving_goals DROP CONSTRAINT IF EXISTS saving_goals_allocation_pct_check;
ALTER TABLE saving_goals DROP COLUMN linked_account_id, DROP COLUMN allocation_pct;
ALTER TABLE saving_goals ADD COLUMN milestones INT[] NOT NULL DEFAULT '{25,50,75,100}',
                         ADD COLUMN closed_at TIMESTAMPTZ;
ALTER TABLE saving_goals DROP CONSTRAINT saving_goals_status_check;
ALTER TABLE saving_goals ADD CONSTRAINT saving_goals_status_check
    CHECK (status IN ('active', 'completed', 'archived'));

CREATE TABLE saving_entries (
    id           UUID PRIMARY KEY,
    user_id      UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id     UUID NOT NULL,
    kind         VARCHAR(10) NOT NULL CHECK (kind IN ('save', 'withdraw', 'move')),
    account_id   UUID NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    from_bucket  VARCHAR(10) NOT NULL CHECK (from_bucket IN ('available', 'spare', 'goal')),
    from_goal_id UUID REFERENCES saving_goals(id) ON DELETE SET NULL,
    to_bucket    VARCHAR(10) NOT NULL CHECK (to_bucket IN ('available', 'spare', 'goal')),
    to_goal_id   UUID REFERENCES saving_goals(id) ON DELETE SET NULL,
    amount       NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    date         DATE NOT NULL,
    note         TEXT,
    source       VARCHAR(10) NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'recurring', 'pending')),
    created_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (
        (kind = 'save' AND from_bucket = 'available' AND to_bucket <> 'available') OR
        (kind = 'withdraw' AND from_bucket <> 'available' AND to_bucket = 'available') OR
        (kind = 'move' AND from_bucket <> 'available' AND to_bucket <> 'available')
    )
);
CREATE INDEX idx_saving_entries_user ON saving_entries (user_id, date DESC, created_at DESC);
CREATE INDEX idx_saving_entries_account ON saving_entries (account_id);
CREATE INDEX idx_saving_entries_from_goal ON saving_entries (from_goal_id) WHERE from_goal_id IS NOT NULL;
CREATE INDEX idx_saving_entries_to_goal ON saving_entries (to_goal_id) WHERE to_goal_id IS NOT NULL;
CREATE TRIGGER set_timestamp_saving_entries BEFORE UPDATE ON saving_entries
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

-- Recurring ออม: a saving setting produces pending saving drafts.
ALTER TABLE recurring_rules DROP CONSTRAINT recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check
    CHECK (type IN ('expense', 'income', 'transfer', 'saving'));
ALTER TABLE recurring_rules ADD COLUMN goal_id UUID REFERENCES saving_goals(id) ON DELETE SET NULL;
ALTER TABLE recurring_items ADD COLUMN saving_entry_id UUID REFERENCES saving_entries(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_recurring_items_saving ON recurring_items (saving_entry_id) WHERE saving_entry_id IS NOT NULL;

-- Pending saving drafts (🐷): submitting one makes a saving entry.
ALTER TABLE pending_transactions DROP CONSTRAINT pending_transactions_kind_check;
ALTER TABLE pending_transactions ADD CONSTRAINT pending_transactions_kind_check
    CHECK (kind IN ('create', 'settle_debt', 'update_tx', 'saving'));
ALTER TABLE pending_transactions DROP CONSTRAINT pending_kind_target;
ALTER TABLE pending_transactions ADD CONSTRAINT pending_kind_target CHECK (
    (kind IN ('create', 'saving') AND target_debt_id IS NULL AND target_transaction_id IS NULL) OR
    (kind = 'settle_debt' AND target_debt_id IS NOT NULL) OR
    (kind = 'update_tx' AND target_transaction_id IS NOT NULL));
