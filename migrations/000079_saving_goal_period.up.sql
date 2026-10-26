-- 0.4.0-5 "เป้าต่อรอบ": a saving goal's target per period (every N days / weeks / months /
-- years, counted from period_anchor). No amount = none. A period's target never goes past what
-- the goal still needs; a short period is not carried into the next one.
ALTER TABLE saving_goals
    ADD COLUMN period_amount NUMERIC(15,2) CHECK (period_amount > 0),
    ADD COLUMN period_freq TEXT CHECK (period_freq IN ('daily', 'weekly', 'monthly', 'yearly')),
    ADD COLUMN period_interval INT NOT NULL DEFAULT 1 CHECK (period_interval BETWEEN 1 AND 99),
    ADD COLUMN period_anchor DATE;
