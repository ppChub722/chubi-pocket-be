-- Best effort: lines added to an item after the up (2+ lines) stay as separate rules.
ALTER TABLE personal_debts DROP COLUMN IF EXISTS recurring_id;
ALTER TABLE recurring_items RENAME CONSTRAINT recurring_items_recurring_rule_id_fkey TO recurring_items_rule_id_fkey;
ALTER INDEX idx_recurring_items_recurring_rule_slot RENAME TO idx_recurring_items_rule_slot;
ALTER INDEX idx_recurring_items_recurring_rule RENAME TO idx_recurring_items_rule;
ALTER TABLE recurring_items RENAME COLUMN recurring_rule_id TO rule_id;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_paid_by_name;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_one_source;
ALTER TABLE recurring_rules DROP COLUMN IF EXISTS paid_by_person_name, DROP COLUMN IF EXISTS paid_by_contact_id;
ALTER TABLE recurring_rules DROP COLUMN IF EXISTS recurring_id;
DROP TABLE IF EXISTS recurrings;
