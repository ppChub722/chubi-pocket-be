-- 0.4.0-1: a saving goal may have no target (open-ended, e.g. saving for a
-- child). NULL target = no %, ETA, behind / on-track status or month_short.
-- The CHECK (target_amount > 0) stays: NULL passes it.
ALTER TABLE saving_goals ALTER COLUMN target_amount DROP NOT NULL;
