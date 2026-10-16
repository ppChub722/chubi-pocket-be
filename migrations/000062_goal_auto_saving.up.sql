-- 0.4.0 M1: auto-saving lives on the goal (and on unallocated: goal_id NULL),
-- not in recurring. One setting per goal: amount, day of month (31 = last day),
-- source wallet or none. On the day a saving draft (🐷) is made.
CREATE TABLE saving_auto (
    id         UUID PRIMARY KEY,
    user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    goal_id    UUID REFERENCES saving_goals(id) ON DELETE CASCADE,
    amount     NUMERIC(15,2) NOT NULL CHECK (amount > 0),
    day        INT NOT NULL CHECK (day BETWEEN 1 AND 31),
    account_id UUID REFERENCES accounts(id) ON DELETE SET NULL,
    -- first day of the month a draft was last made for (one draft per month)
    last_month DATE,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX idx_saving_auto_goal ON saving_auto (user_id, goal_id) NULLS NOT DISTINCT;
CREATE TRIGGER set_timestamp_saving_auto BEFORE UPDATE ON saving_auto
    FOR EACH ROW EXECUTE FUNCTION set_timestamp();

-- The old recurring ออม settings move here (data is disposable: one setting
-- per goal, the oldest wins) and the settings go.
INSERT INTO saving_auto (id, user_id, goal_id, amount, day, account_id, created_at)
SELECT DISTINCT ON (user_id, goal_id) gen_random_uuid(), user_id, goal_id, amount,
       COALESCE(month_day, EXTRACT(DAY FROM start_date)::int), account_id, created_at
FROM recurring_rules WHERE type = 'saving' AND status = 'active'
ORDER BY user_id, goal_id, created_at;
DELETE FROM recurring_rules WHERE type = 'saving';
