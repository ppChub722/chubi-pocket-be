-- Back to one monthly setting per goal: the oldest of each stays.
DELETE FROM saving_auto a USING saving_auto b
WHERE a.user_id = b.user_id AND a.goal_id IS NOT DISTINCT FROM b.goal_id
  AND (a.created_at, a.id) > (b.created_at, b.id);

ALTER TABLE saving_auto
    ADD COLUMN day        INT,
    ADD COLUMN last_month DATE;

UPDATE saving_auto SET
    day = COALESCE((rule->'monthly'->>'day')::int, EXTRACT(DAY FROM start_date)::int),
    last_month = CASE WHEN last_produced IS NULL THEN NULL ELSE date_trunc('month', last_produced)::date END;

ALTER TABLE saving_auto
    ALTER COLUMN day SET NOT NULL,
    ADD CONSTRAINT saving_auto_day_check CHECK (day BETWEEN 1 AND 31),
    DROP COLUMN rule,
    DROP COLUMN start_date,
    DROP COLUMN end_rule,
    DROP COLUMN last_produced;

DROP INDEX IF EXISTS idx_saving_auto_goal;
CREATE UNIQUE INDEX idx_saving_auto_goal ON saving_auto (user_id, goal_id) NULLS NOT DISTINCT;
