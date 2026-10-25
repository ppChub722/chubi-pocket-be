-- Back to goal auto-saves (best effort): every recurring saving setting with an
-- amount becomes a saving_auto row again (its occurrences go; the saving entries stay).
CREATE TABLE saving_auto (
    id            UUID PRIMARY KEY,
    user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    goal_id       UUID REFERENCES saving_goals(id) ON DELETE CASCADE,
    amount        NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    account_id    UUID REFERENCES accounts(id) ON DELETE SET NULL,
    rule          JSONB NOT NULL,
    start_date    DATE NOT NULL,
    end_rule      JSONB NOT NULL DEFAULT '{"kind":"none","date":null,"count":null}',
    last_produced DATE,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX idx_saving_auto_goal ON saving_auto (user_id, goal_id);
CREATE TRIGGER set_timestamp_saving_auto BEFORE UPDATE ON saving_auto
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

INSERT INTO saving_auto (id, user_id, goal_id, amount, account_id, rule, start_date, end_rule, last_produced, created_at)
SELECT r.id, r.user_id, r.goal_id, r.amount, r.account_id,
       jsonb_build_object(
           'freq', r.freq, 'interval', r.interval_n, 'weekdays', to_jsonb(r.weekdays),
           'monthly', CASE WHEN r.freq = 'monthly' THEN jsonb_build_object(
               'mode', COALESCE(r.monthly_mode, 'day'),
               'day', CASE WHEN COALESCE(r.monthly_mode, 'day') = 'day' THEN COALESCE(r.month_day, EXTRACT(DAY FROM r.start_date)::int) END,
               'nth', r.month_nth, 'weekday', r.month_weekday) END,
           'dates', COALESCE((SELECT jsonb_agg(to_char(d, 'YYYY-MM-DD')) FROM unnest(r.dates) d), '[]'::jsonb)),
       r.start_date,
       jsonb_build_object('kind', r.end_kind, 'date', to_char(r.end_date, 'YYYY-MM-DD'), 'count', r.end_count),
       GREATEST(r.tracked_from - 1, (SELECT MAX(COALESCE(i.slot, i.date)) FROM recurring_items i WHERE i.rule_id = r.id)),
       r.created_at
FROM recurring_rules r
WHERE r.type = 'saving' AND r.amount > 0 AND r.status <> 'archived';

DELETE FROM recurring_rules WHERE type = 'saving';

DROP INDEX IF EXISTS idx_recurring_rules_goal;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_goal_id_fkey;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_goal_id_fkey
    FOREIGN KEY (goal_id) REFERENCES saving_goals(id) ON DELETE SET NULL;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_kind_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_kind_check
    CHECK (kind IN ('subscription', 'installment', 'income', 'reminder'));
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check
    CHECK (type IN ('expense', 'income', 'transfer'));
