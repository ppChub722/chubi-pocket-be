ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check CHECK (type IN ('expense', 'income', 'transfer', 'saving'));
UPDATE recurring_rules SET amount = 1 WHERE amount = 0;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_amount_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_amount_check CHECK (amount > 0);
ALTER TABLE recurring_rules DROP COLUMN trial_until, DROP COLUMN kind;
