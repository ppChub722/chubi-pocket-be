-- 0.4.0-4 "แก้เฉพาะรอบนี้": what a period would be without its own amount (the budget's amount for
-- that period). "ใช้งบปกติ" puts it back, so a past period never takes today's amount.
ALTER TABLE budget_periods ADD COLUMN base_amount NUMERIC(15,2);
UPDATE budget_periods p
SET base_amount = CASE WHEN p.overridden THEN b.amount ELSE p.amount END
FROM budgets b
WHERE b.id = p.budget_id;
ALTER TABLE budget_periods ALTER COLUMN base_amount SET NOT NULL;
ALTER TABLE budget_periods ADD CONSTRAINT budget_periods_base_amount_check CHECK (base_amount > 0);
