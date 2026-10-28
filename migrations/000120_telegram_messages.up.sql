-- 0.4.2 1a (owner OK 2026-10-28): every text an allowed private chat sends the Telegram bot, kept
-- once per update_id (Telegram may resend). The real-sentence set for the LLM eval (1b); no user
-- link yet (1c adds linking). Pre-release data, wipe-able.
CREATE TABLE telegram_messages (
    id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    update_id     BIGINT      NOT NULL UNIQUE,
    chat_id       BIGINT      NOT NULL,
    from_username TEXT,
    text          TEXT        NOT NULL,
    received_at   TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_telegram_messages_chat ON telegram_messages (chat_id, received_at DESC);
