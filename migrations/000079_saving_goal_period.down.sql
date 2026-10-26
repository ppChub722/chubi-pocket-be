ALTER TABLE saving_goals
    DROP COLUMN IF EXISTS period_amount,
    DROP COLUMN IF EXISTS period_freq,
    DROP COLUMN IF EXISTS period_interval,
    DROP COLUMN IF EXISTS period_anchor;
