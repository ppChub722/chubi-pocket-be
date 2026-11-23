ALTER TABLE recurrings DROP CONSTRAINT IF EXISTS recurrings_debt_target;
ALTER TABLE recurrings DROP COLUMN IF EXISTS counterparty_account_id, DROP COLUMN IF EXISTS debt_plan_id;
ALTER TABLE recurrings ADD CONSTRAINT recurrings_counterparty_check CHECK (
    (type = 'debt') = (counterparty_person_name IS NOT NULL OR counterparty_institution_id IS NOT NULL)
    AND (counterparty_contact_id IS NULL OR counterparty_institution_id IS NULL)
    AND (type = 'debt' OR (counterparty_contact_id IS NULL AND debt_direction IS NULL)));
ALTER TABLE personal_debts DROP COLUMN IF EXISTS debt_plan_id;
DROP TABLE IF EXISTS debt_installments;
DROP TABLE IF EXISTS debt_plans;
