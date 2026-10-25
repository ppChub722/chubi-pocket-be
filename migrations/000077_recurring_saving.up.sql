-- 0.4.0-4: saving is a recurring kind again ("ออมเงิน": money from a wallet into a
-- goal, or into the unallocated pot when goal_id is NULL). It replaces the goal
-- auto-saves (saving_auto): every auto-save becomes a recurring saving setting.
-- The saving drafts the auto-saves already made stay in pending as they are.

ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check
    CHECK (type IN ('expense', 'income', 'transfer', 'saving'));
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_kind_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_kind_check
    CHECK (kind IN ('subscription', 'installment', 'income', 'reminder', 'saving'));

-- Deleting a goal deletes its saving settings (the service does it too).
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_goal_id_fkey;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_goal_id_fkey
    FOREIGN KEY (goal_id) REFERENCES saving_goals(id) ON DELETE CASCADE;
CREATE INDEX IF NOT EXISTS idx_recurring_rules_goal ON recurring_rules (goal_id) WHERE goal_id IS NOT NULL;

-- saving_auto.rule / end_rule are the /recurring API shapes:
--   rule {freq, interval, weekdays[], monthly {mode, day, nth, weekday} | null, dates[]}
--   end_rule {kind none|date|count, date, count}
-- tracked_from: occurrences up to last_produced already have drafts, so they are
-- history, never due. A setting on a goal that is not active starts paused.
-- total_count / generated_until of a setting with an end are worked out by the
-- app server on read (recurring scanRule) and saved with its next write.
INSERT INTO recurring_rules (
    id, user_id, name, description, note, icon_code, type, kind, amount,
    account_id, to_account_id, category_id, tag_ids, goal_id,
    freq, interval_n, weekdays, monthly_mode, month_day, month_nth, month_weekday, dates,
    start_date, end_kind, end_date, end_count, total_count, generated_until,
    status, ending_notified, tracked_from, due_notified_until, auto_create, auto_created_until, paused_from,
    created_at, updated_at)
SELECT
    a.id, a.user_id,
    CASE WHEN a.goal_id IS NULL OR g.name IS NULL THEN 'ออมเงิน' ELSE left('ออม ' || g.name, 100) END,
    NULL, NULL, NULL, 'saving', 'saving', a.amount,
    a.account_id, NULL, NULL, '{}'::uuid[], a.goal_id,
    a.rule->>'freq',
    GREATEST(COALESCE((a.rule->>'interval')::int, 1), 1),
    COALESCE(ARRAY(SELECT w::int FROM jsonb_array_elements_text(COALESCE(a.rule->'weekdays', '[]'::jsonb)) w), '{}'),
    CASE WHEN a.rule->>'freq' = 'monthly' THEN COALESCE(a.rule->'monthly'->>'mode', 'day') END,
    CASE WHEN a.rule->>'freq' = 'monthly' AND COALESCE(a.rule->'monthly'->>'mode', 'day') = 'day'
         THEN (a.rule->'monthly'->>'day')::int END,
    CASE WHEN a.rule->>'freq' = 'monthly' AND a.rule->'monthly'->>'mode' = 'weekday'
         THEN (a.rule->'monthly'->>'nth')::int END,
    CASE WHEN a.rule->>'freq' = 'monthly' AND a.rule->'monthly'->>'mode' = 'weekday'
         THEN (a.rule->'monthly'->>'weekday')::int END,
    COALESCE(ARRAY(SELECT d::date FROM jsonb_array_elements_text(COALESCE(a.rule->'dates', '[]'::jsonb)) d), '{}'),
    a.start_date,
    COALESCE(NULLIF(a.end_rule->>'kind', ''), 'none'),
    CASE WHEN a.end_rule->>'kind' = 'date' THEN (a.end_rule->>'date')::date END,
    CASE WHEN a.end_rule->>'kind' = 'count' THEN (a.end_rule->>'count')::int END,
    NULL, NULL,
    CASE WHEN a.goal_id IS NULL OR g.status = 'active' THEN 'active' ELSE 'paused' END,
    FALSE,
    COALESCE(a.last_produced + 1, a.start_date),
    NULL, FALSE, NULL,
    CASE WHEN a.goal_id IS NULL OR g.status = 'active' THEN NULL ELSE CURRENT_DATE END,
    a.created_at, NOW()
FROM saving_auto a
LEFT JOIN saving_goals g ON g.id = a.goal_id
WHERE a.rule->>'freq' IN ('daily', 'weekly', 'monthly', 'yearly', 'dates')
ON CONFLICT (id) DO NOTHING;

DROP TABLE saving_auto;
