-- A goal (or the unallocated pot) can have many auto-saves, each with its own
-- wallet, amount and a full recurrence rule (the /recurring shapes). The
-- rule + start + end are kept as the API shapes; last_produced is the latest
-- occurrence a draft was made for.
DROP INDEX IF EXISTS idx_saving_auto_goal;
CREATE INDEX idx_saving_auto_goal ON saving_auto (user_id, goal_id);

ALTER TABLE saving_auto
    ADD COLUMN rule          JSONB,
    ADD COLUMN start_date    DATE,
    ADD COLUMN end_rule      JSONB NOT NULL DEFAULT '{"kind":"none","date":null,"count":null}',
    ADD COLUMN last_produced DATE;

UPDATE saving_auto SET
    rule = jsonb_build_object(
        'freq', 'monthly', 'interval', 1, 'weekdays', '[]'::jsonb,
        'monthly', jsonb_build_object('mode', 'day', 'day', day, 'nth', NULL, 'weekday', NULL),
        'dates', '[]'::jsonb),
    start_date = created_at::date,
    last_produced = CASE WHEN last_month IS NULL THEN NULL ELSE
        LEAST(last_month + (day - 1), (date_trunc('month', last_month) + INTERVAL '1 month - 1 day')::date) END;

ALTER TABLE saving_auto
    ALTER COLUMN rule SET NOT NULL,
    ALTER COLUMN start_date SET NOT NULL,
    DROP COLUMN day,
    DROP COLUMN last_month;
