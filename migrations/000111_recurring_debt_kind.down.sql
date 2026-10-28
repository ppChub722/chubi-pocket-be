DELETE FROM recurring_rules WHERE type = 'debt';

DROP INDEX IF EXISTS idx_recurring_items_debt_entry;
ALTER TABLE recurring_items DROP COLUMN IF EXISTS debt_entry_id;

DROP INDEX IF EXISTS idx_recurring_rules_contact;
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_debt_fields_check;
ALTER TABLE recurring_rules
    DROP COLUMN IF EXISTS contact_id,
    DROP COLUMN IF EXISTS person_name,
    DROP COLUMN IF EXISTS debt_kind,
    DROP COLUMN IF EXISTS debt_direction,
    DROP COLUMN IF EXISTS zero_rounds;

UPDATE recurring_rules SET pause_reason = 'manual' WHERE pause_reason = 'no_balance';
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_pause_reason_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_pause_reason_check
    CHECK (pause_reason IN ('manual', 'overdue_limit', 'goal_inactive'));
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_kind_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_kind_check
    CHECK (kind IN ('subscription', 'installment', 'income', 'reminder', 'saving'));
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check
    CHECK (type IN ('expense', 'income', 'transfer', 'saving'));
