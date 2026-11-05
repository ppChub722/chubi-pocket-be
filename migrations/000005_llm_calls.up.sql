-- Chatbot usage (0.4.2-4, K2). users.tier: who may use the chatbot (free = not yet; owner = yes, with
-- the caps); set by SQL for now. llm_calls: one row per model call (tokens, ms, status), never the
-- message text; the monthly cap and any later billing read it.
ALTER TABLE users ADD COLUMN tier character varying(16) NOT NULL DEFAULT 'free';
ALTER TABLE users ADD CONSTRAINT users_tier_check CHECK (tier IN ('free', 'owner'));

CREATE TABLE llm_calls (
    id uuid PRIMARY KEY,
    user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    channel character varying(16) NOT NULL,
    provider character varying(16) NOT NULL,
    model character varying(64) NOT NULL,
    tokens_in integer NOT NULL DEFAULT 0,
    tokens_out integer NOT NULL DEFAULT 0,
    cache_read integer NOT NULL DEFAULT 0,
    cache_write integer NOT NULL DEFAULT 0,
    ms integer NOT NULL DEFAULT 0,
    status character varying(32) NOT NULL,
    created_at timestamp with time zone NOT NULL DEFAULT now()
);
CREATE INDEX idx_llm_calls_user_month ON llm_calls (user_id, created_at);
