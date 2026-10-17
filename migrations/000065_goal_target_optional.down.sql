-- Back to a required target: open-ended goals get the money they hold as
-- their target (at least 0.01), so none of them changes what it shows saved.
UPDATE saving_goals g SET target_amount = GREATEST(0.01, COALESCE((
    SELECT SUM(CASE WHEN e.to_goal_id = g.id THEN e.amount ELSE -e.amount END)
    FROM saving_entries e
    WHERE e.from_goal_id = g.id OR e.to_goal_id = g.id), 0))
WHERE target_amount IS NULL;
ALTER TABLE saving_goals ALTER COLUMN target_amount SET NOT NULL;
