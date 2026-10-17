-- 0.4.0 M4: recurring has four KINDS (what the user picks): subscription,
-- installment (a count of payments), income, reminder (เตือนจ่าย: the amount
-- may be unknown). `type` stays the money direction (expense / income /
-- transfer). The old ออม / บิล / โอน types go: saving moved to the goals
-- (migration 062), a bill is a subscription (an installment when it has a
-- count), a transfer is a reminder. No amount = 0 on a reminder.
ALTER TABLE recurring_rules
    ADD COLUMN kind VARCHAR(12) NOT NULL DEFAULT 'subscription'
        CHECK (kind IN ('subscription', 'installment', 'income', 'reminder')),
    ADD COLUMN trial_until DATE;

UPDATE recurring_rules SET kind = CASE
    WHEN type = 'income' THEN 'income'
    WHEN type = 'transfer' THEN 'reminder'
    WHEN end_kind = 'count' THEN 'installment'
    ELSE 'subscription' END;

ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_amount_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_amount_check CHECK (amount >= 0);
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check CHECK (type IN ('expense', 'income', 'transfer'));
