ALTER TABLE personal_debts DROP CONSTRAINT IF EXISTS personal_debts_one_counterparty;
ALTER TABLE personal_debts DROP COLUMN IF EXISTS counterparty_institution_id;
ALTER TABLE accounts DROP COLUMN IF EXISTS institution_id;
DROP TABLE IF EXISTS institutions;
DELETE FROM payment_providers WHERE scheme = 'chubi' OR (scheme = 'bot' AND code IN ('025', '011', '030'));
ALTER TABLE payment_providers DROP COLUMN IF EXISTS phone, DROP COLUMN IF EXISTS website;
ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_kind_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_kind_check
    CHECK (kind IN ('bank', 'e_wallet', 'card_issuer', 'other'));
