-- Payment providers (owner design 2026-10-09): who a slip's money moved
-- through — banks first, later e-wallets and card issuers. `scheme` says
-- whose code `code` is ("bot" = the Bank of Thailand's 3-digit bank code,
-- what a slip's QR carries), so other code systems can join later without
-- clashing. Seeded only with codes confirmed from real slip QRs / sources;
-- unknown codes met on slips are written to import_logs for us to add.
CREATE TABLE IF NOT EXISTS payment_providers (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    kind        VARCHAR(20)  NOT NULL
                  CHECK (kind IN ('bank', 'e_wallet', 'card_issuer', 'other')),
    scheme      VARCHAR(20)  NOT NULL,
    code        VARCHAR(20)  NOT NULL,
    name_th     TEXT         NOT NULL,
    name_en     TEXT         NOT NULL,
    short_name  VARCHAR(20),
    color       VARCHAR(9),  -- "#rrggbb", optional
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT payment_providers_scheme_code UNIQUE (scheme, code)
);

CREATE TRIGGER set_timestamp_payment_providers
BEFORE UPDATE ON payment_providers
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();

INSERT INTO payment_providers (kind, scheme, code, name_th, name_en, short_name) VALUES
    ('bank', 'bot', '002', 'ธนาคารกรุงเทพ',    'Bangkok Bank',         'BBL'),
    ('bank', 'bot', '004', 'ธนาคารกสิกรไทย',   'Kasikornbank',         'KBANK'),
    ('bank', 'bot', '006', 'ธนาคารกรุงไทย',    'Krungthai Bank',       'KTB'),
    ('bank', 'bot', '014', 'ธนาคารไทยพาณิชย์', 'Siam Commercial Bank', 'SCB')
ON CONFLICT (scheme, code) DO NOTHING;
