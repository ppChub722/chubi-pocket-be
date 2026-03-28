-- Add missing fields to accounts table (per design spec)
-- Expand type to support e_wallet and pay_later
ALTER TABLE accounts ADD COLUMN icon VARCHAR(50);
ALTER TABLE accounts ADD COLUMN is_active BOOLEAN NOT NULL DEFAULT true;
ALTER TABLE accounts ADD COLUMN credit_limit DECIMAL(15,2);
ALTER TABLE accounts ADD COLUMN statement_date INTEGER;
ALTER TABLE accounts ADD COLUMN payment_due_date INTEGER;
ALTER TABLE accounts ADD COLUMN minimum_payment DECIMAL(15,2);

-- Rename current_balance to balance to match spec
ALTER TABLE accounts RENAME COLUMN current_balance TO balance;
-- Drop initial_balance (spec uses single balance field)
ALTER TABLE accounts DROP COLUMN IF EXISTS initial_balance;
