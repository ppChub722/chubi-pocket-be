-- 0.4.1-1 R7: why a recurring setting is paused. overdue_limit = it held 3 due
-- occurrences when a 4th came due ("ค้าง 3 งวด"); goal_inactive = a saving
-- setting whose goal was closed / archived; manual = the user paused it.
-- An archived wallet no longer pauses anything (R12); the settings it paused
-- before stay paused, as manual.
ALTER TABLE recurring_rules ADD COLUMN pause_reason TEXT
    CONSTRAINT recurring_rules_pause_reason_check
    CHECK (pause_reason IN ('manual', 'overdue_limit', 'goal_inactive'));

UPDATE recurring_rules r SET pause_reason = CASE
        WHEN r.type = 'saving' AND EXISTS (SELECT 1 FROM saving_goals g WHERE g.id = r.goal_id AND g.status <> 'active')
            THEN 'goal_inactive'
        ELSE 'manual' END
WHERE r.status = 'paused';

-- The settings already over the limit pause at their first due check after the
-- release (owner answer 5): every user's check runs again.
DELETE FROM recurring_due_checks;

-- 0.4.1-1 R8 / R9: resuming makes the current period's occurrence the next one;
-- the occurrences between the pause and it are never made. gaps = those spans
-- [{"from": "YYYY-MM-DD", "to": "YYYY-MM-DD"}] (from <= slot < to): the schedule
-- skips them and a count end (an installment) counts past them.
ALTER TABLE recurring_rules ADD COLUMN gaps JSONB NOT NULL DEFAULT '[]'::jsonb;
