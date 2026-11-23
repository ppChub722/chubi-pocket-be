-- Best effort: reminders added to an item after the up (2+ reminders) stay as separate rules.
UPDATE pending_transactions SET draft = (draft - 'recurring_reminder_id') || jsonb_build_object('recurring_id', draft->'recurring_reminder_id')
WHERE draft ? 'recurring_reminder_id';
ALTER TABLE personal_debts DROP COLUMN IF EXISTS recurring_id;
ALTER TABLE recurring_items RENAME CONSTRAINT recurring_items_recurring_reminder_id_fkey TO recurring_items_rule_id_fkey;
ALTER INDEX idx_recurring_items_reminder_slot RENAME TO idx_recurring_items_rule_slot;
ALTER INDEX idx_recurring_items_reminder RENAME TO idx_recurring_items_rule;
ALTER TABLE recurring_items RENAME COLUMN recurring_reminder_id TO rule_id;
ALTER INDEX idx_recurring_reminders_recurring RENAME TO idx_recurring_rules_recurring;
ALTER TABLE recurring_reminders RENAME CONSTRAINT recurring_reminders_pkey TO recurring_rules_pkey;
ALTER TABLE recurring_reminders RENAME TO recurring_rules;
ALTER TABLE recurring_rules DROP COLUMN IF EXISTS repays_reminder_id, DROP COLUMN IF EXISTS amount_mode;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_paid_by_name;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_one_source;
ALTER TABLE recurring_rules DROP COLUMN IF EXISTS paid_by_person_name, DROP COLUMN IF EXISTS paid_by_contact_id;
ALTER TABLE recurring_rules DROP COLUMN IF EXISTS recurring_id;
DROP TABLE IF EXISTS recurrings;
