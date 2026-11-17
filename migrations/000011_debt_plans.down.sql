DROP TABLE IF EXISTS debt_installments;
ALTER TABLE recurrings DROP CONSTRAINT IF EXISTS recurrings_plan_check,
    DROP COLUMN IF EXISTS plan_principal, DROP COLUMN IF EXISTS plan_count, DROP COLUMN IF EXISTS plan_start,
    DROP COLUMN IF EXISTS plan_interest_method, DROP COLUMN IF EXISTS plan_interest_rate,
    DROP COLUMN IF EXISTS plan_installment_amount, DROP COLUMN IF EXISTS card_account_id, DROP COLUMN IF EXISTS plan_closed_at;
