ALTER TABLE accounts ADD COLUMN IF NOT EXISTS initial_balance DECIMAL(19,4) NOT NULL DEFAULT 0;
ALTER TABLE accounts RENAME COLUMN balance TO current_balance;
ALTER TABLE accounts DROP COLUMN IF EXISTS minimum_payment;
ALTER TABLE accounts DROP COLUMN IF EXISTS payment_due_date;
ALTER TABLE accounts DROP COLUMN IF EXISTS statement_date;
ALTER TABLE accounts DROP COLUMN IF EXISTS credit_limit;
ALTER TABLE accounts DROP COLUMN IF EXISTS is_active;
ALTER TABLE accounts DROP COLUMN IF EXISTS icon;
