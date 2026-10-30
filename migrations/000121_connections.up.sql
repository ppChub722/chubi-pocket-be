-- 0.4.2 1b (owner 2026-10-28): outside apps linked to a user (Telegram now; LINE / Google later).
-- Linked = allowed: the bot answers a chat only when it is linked here (TELEGRAM_ALLOWED_CHAT_IDS
-- is retired). Additive only: prod data is real.
CREATE TABLE user_connections (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id       UUID        NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    provider      VARCHAR(20) NOT NULL CHECK (provider IN ('telegram', 'line', 'google')),
    -- the user in that app: Telegram = the private chat id (= the Telegram user id)
    external_id   TEXT        NOT NULL,
    -- who it is there, as shown: Telegram "@username", else the first name
    account_label TEXT,
    linked_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    -- {key: bool}, e.g. {"chat": true}; a missing key = off
    permissions   JSONB       NOT NULL DEFAULT '{}'::jsonb,
    created_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (provider, external_id),
    UNIQUE (user_id, provider)
);

CREATE TRIGGER set_timestamp_user_connections
BEFORE UPDATE ON user_connections
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();

-- One-time codes for the t.me start payload (single use). Only the SHA-256 of the code is kept;
-- the code itself (128-bit random, base64url, 22 chars) travels in the payload.
--   purpose link  (1b):   a signed-in user links an app; 10 minutes; user_id = that user.
--   purpose login (1b-2): sign up / log in with the app; 5 minutes; PKCE S256 code_challenge;
--                         the bot fills external_id / account_label (and user_id when that chat is
--                         linked) once the user confirms in the chat (confirmed_at).
CREATE TABLE connection_link_codes (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    code_hash      BYTEA       NOT NULL UNIQUE,
    purpose        VARCHAR(10) NOT NULL DEFAULT 'link' CHECK (purpose IN ('link', 'login')),
    user_id        UUID REFERENCES users (id) ON DELETE CASCADE,
    provider       VARCHAR(20) NOT NULL CHECK (provider IN ('telegram', 'line', 'google')),
    code_challenge TEXT,
    external_id    TEXT,
    account_label  TEXT,
    external_name  TEXT, -- login: the name in that app (Telegram first + last), the sign-up prefill
    confirmed_at   TIMESTAMPTZ,
    expires_at     TIMESTAMPTZ NOT NULL,
    used_at        TIMESTAMPTZ,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK (purpose = 'login' OR user_id IS NOT NULL),
    CHECK (purpose = 'link' OR code_challenge IS NOT NULL)
);

CREATE INDEX idx_connection_link_codes_user ON connection_link_codes (user_id, provider);

-- Kept texts now know whose they are (null = sent before linking existed).
ALTER TABLE telegram_messages
    ADD COLUMN user_id UUID REFERENCES users (id) ON DELETE SET NULL;
