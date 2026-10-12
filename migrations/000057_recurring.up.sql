-- 0.3.1-10 recurring (owner 2026-10-13, FINAL; replaces scheduled — its
-- data is dropped, nobody uses it). Two parts:
--   recurring_rules = the setting / generator (Google-Calendar-style rule)
--   recurring_items = what it produces (+ one-off upcoming items and manual
--     items): the module's own records, LINKED to a pending draft or a real
--     transaction the same way an event bill links its transaction (the link
--     lives on the item, ON DELETE SET NULL breaks it).
-- Status is derived: transaction_id → done, pending_id → pending, archived
-- (user flag), produced_at NULL → upcoming, else not_created.

ALTER TABLE transactions DROP COLUMN IF EXISTS scheduled_transaction_id;
DROP TABLE IF EXISTS scheduled_transactions CASCADE;

CREATE TABLE recurring_rules (
    id              UUID PRIMARY KEY,
    user_id         UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name            VARCHAR(100) NOT NULL,
    description     TEXT,
    note            TEXT,
    icon_code       JSONB,
    type            VARCHAR(10) NOT NULL CHECK (type IN ('expense', 'income', 'transfer')),
    amount          NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    account_id      UUID REFERENCES accounts(id) ON DELETE SET NULL,
    to_account_id   UUID REFERENCES accounts(id) ON DELETE SET NULL,
    category_id     UUID REFERENCES categories(id) ON DELETE SET NULL,
    tag_ids         UUID[] NOT NULL DEFAULT '{}',
    freq            VARCHAR(10) NOT NULL CHECK (freq IN ('daily', 'weekly', 'monthly', 'yearly', 'dates')),
    interval_n      INT NOT NULL DEFAULT 1 CHECK (interval_n >= 1),
    weekdays        INT[] NOT NULL DEFAULT '{}',
    monthly_mode    VARCHAR(10) CHECK (monthly_mode IS NULL OR monthly_mode IN ('day', 'weekday')),
    month_day       INT,
    month_nth       INT,
    month_weekday   INT,
    dates           DATE[] NOT NULL DEFAULT '{}',
    start_date      DATE NOT NULL,
    end_kind        VARCHAR(5) NOT NULL DEFAULT 'none' CHECK (end_kind IN ('none', 'date', 'count')),
    end_date        DATE,
    end_count       INT,
    total_count     INT,
    generated_until DATE,
    status          VARCHAR(10) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'paused', 'archived')),
    ending_notified BOOLEAN NOT NULL DEFAULT FALSE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_recurring_rules_user ON recurring_rules (user_id, status);
CREATE TRIGGER set_timestamp_recurring_rules BEFORE UPDATE ON recurring_rules
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

CREATE TABLE recurring_items (
    id             UUID PRIMARY KEY,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    rule_id        UUID REFERENCES recurring_rules(id) ON DELETE CASCADE,
    seq            INT,
    -- slot: the rule date a generated item was made for (its date may move;
    -- NULL for manual and one-off items).
    slot           DATE,
    date           DATE NOT NULL,
    draft          JSONB NOT NULL,
    edited         BOOLEAN NOT NULL DEFAULT FALSE,
    manual         BOOLEAN NOT NULL DEFAULT FALSE,
    archived       BOOLEAN NOT NULL DEFAULT FALSE,
    produced_at    TIMESTAMPTZ,
    pending_id     UUID REFERENCES pending_transactions(id) ON DELETE SET NULL,
    transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_recurring_items_user_date ON recurring_items (user_id, date);
CREATE INDEX idx_recurring_items_rule ON recurring_items (rule_id, date);
-- One generated item per slot of a setting (generation is idempotent).
CREATE UNIQUE INDEX idx_recurring_items_rule_slot ON recurring_items (rule_id, slot)
    WHERE slot IS NOT NULL;
CREATE UNIQUE INDEX idx_recurring_items_pending ON recurring_items (pending_id) WHERE pending_id IS NOT NULL;
CREATE UNIQUE INDEX idx_recurring_items_tx ON recurring_items (transaction_id) WHERE transaction_id IS NOT NULL;
CREATE TRIGGER set_timestamp_recurring_items BEFORE UPDATE ON recurring_items
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN (
    'split_created', 'split_paid', 'split_received', 'split_changed',
    'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite',
    'contact_link_request', 'account_invite', 'project_added',
    'recurring_due', 'recurring_ending'));
