-- 0.4.3 debts: institutions (the user's own banks / card issuers / finance companies / stores),
-- optionally tied to a central payment provider; a wallet and a debt row may point at one.

ALTER TABLE payment_providers DROP CONSTRAINT payment_providers_kind_check;
ALTER TABLE payment_providers ADD CONSTRAINT payment_providers_kind_check
    CHECK (kind IN ('bank', 'e_wallet', 'card_issuer', 'finance', 'store', 'other'));
ALTER TABLE payment_providers ADD COLUMN phone varchar(30), ADD COLUMN website text;

UPDATE payment_providers SET phone = '1333' WHERE scheme = 'bot' AND code = '002';
UPDATE payment_providers SET phone = '02-888-8888', website = 'https://www.kasikornbank.com' WHERE scheme = 'bot' AND code = '004';
UPDATE payment_providers SET phone = '02-111-1111', website = 'https://krungthai.com' WHERE scheme = 'bot' AND code = '006';
UPDATE payment_providers SET phone = '02-777-7777', website = 'https://www.scb.co.th' WHERE scheme = 'bot' AND code = '014';
UPDATE payment_providers SET website = 'https://www.bangkokbank.com' WHERE scheme = 'bot' AND code = '002';

INSERT INTO payment_providers (kind, scheme, code, name_th, name_en, short_name, phone, website) VALUES
    ('bank', 'bot', '025', 'ธนาคารกรุงศรีอยุธยา', 'Bank of Ayudhya (Krungsri)', 'BAY', '1572', 'https://www.krungsri.com'),
    ('bank', 'bot', '011', 'ธนาคารทหารไทยธนชาต', 'TMBThanachart Bank', 'TTB', '1428', 'https://www.ttbbank.com'),
    ('bank', 'bot', '030', 'ธนาคารออมสิน', 'Government Savings Bank', 'GSB', '1115', 'https://www.gsb.or.th'),
    ('card_issuer', 'chubi', 'ktc', 'เคทีซี (บัตรกรุงไทย)', 'KTC (Krungthai Card)', 'KTC', '02-123-5000', 'https://www.ktc.co.th'),
    ('card_issuer', 'chubi', 'firstchoice', 'กรุงศรี เฟิร์สช้อยส์', 'Krungsri First Choice', 'First Choice', NULL, NULL),
    ('card_issuer', 'chubi', 'aeon', 'อิออน', 'AEON Thana Sinsap', 'AEON', NULL, 'https://www.aeon.co.th'),
    ('store', 'chubi', 'spaylater', 'ช้อปปี้ เพย์เลเตอร์', 'Shopee SPayLater', 'SPayLater', NULL, NULL),
    ('finance', 'chubi', 'kleasing', 'ลีสซิ่งกสิกรไทย', 'Kasikorn Leasing', 'K-Leasing', NULL, NULL),
    ('finance', 'chubi', 'krungsriauto', 'กรุงศรี ออโต้', 'Krungsri Auto', 'Krungsri Auto', NULL, NULL),
    ('finance', 'chubi', 'ttbdrive', 'ทีทีบี ไดรฟ์', 'ttb drive', 'ttb drive', NULL, NULL),
    ('finance', 'chubi', 'tidlor', 'เงินติดล้อ', 'Ngern Tid Lor', 'Tidlor', NULL, NULL),
    ('finance', 'chubi', 'muangthai', 'เมืองไทย แคปปิตอล', 'Muangthai Capital', 'MTC', NULL, NULL)
ON CONFLICT (scheme, code) DO NOTHING;
-- Phones / websites: only the ones I was sure of; the rest are left for the owner to fill.

CREATE TABLE institutions (
    id           uuid PRIMARY KEY,
    user_id      uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name         varchar(100) NOT NULL,
    kind         varchar(20) NOT NULL CHECK (kind IN ('bank', 'card', 'finance', 'store', 'other')),
    provider_id  uuid REFERENCES payment_providers(id) ON DELETE SET NULL,
    phone        varchar(30),
    email        varchar(200),
    icon_code    jsonb,
    description  text,
    note         text,
    status       varchar(20) NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),
    created_at   timestamptz NOT NULL DEFAULT now(),
    updated_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX idx_institutions_user ON institutions (user_id, status);
-- one own row per central provider (POST with a provider_id reuses it)
CREATE UNIQUE INDEX idx_institutions_user_provider ON institutions (user_id, provider_id) WHERE provider_id IS NOT NULL;
CREATE TRIGGER set_timestamp_institutions BEFORE UPDATE ON institutions FOR EACH ROW EXECUTE FUNCTION set_timestamp();

ALTER TABLE accounts ADD COLUMN institution_id uuid REFERENCES institutions(id) ON DELETE SET NULL;
CREATE INDEX idx_accounts_institution ON accounts (institution_id) WHERE institution_id IS NOT NULL;

-- a debt row's counterparty: a contact, a typed name or an institution. The name stays as the
-- display copy (as for a contact); a contact and an institution never both.
ALTER TABLE personal_debts ADD COLUMN counterparty_institution_id uuid REFERENCES institutions(id) ON DELETE RESTRICT;
ALTER TABLE personal_debts ADD CONSTRAINT personal_debts_one_counterparty
    CHECK (counterparty_contact_id IS NULL OR counterparty_institution_id IS NULL);
CREATE INDEX idx_personal_debts_institution ON personal_debts (counterparty_institution_id) WHERE counterparty_institution_id IS NOT NULL;
