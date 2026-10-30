-- 0.4.2-3 (owner 2026-10-30): safe retries. The app sends one Idempotency-Key per save and the same key
-- on every retry; the first answer is kept here so a retry after a timeout returns it instead of
-- writing twice. Additive (a new table): no existing row changes. Rows are throwaway (the middleware
-- deletes the ones older than 24 hours).
CREATE TABLE idempotency_keys (
    user_id       UUID         NOT NULL REFERENCES users (id) ON DELETE CASCADE,
    key           VARCHAR(200) NOT NULL,
    -- sha256(method + request URI + body): the same key for a different request is refused
    request_hash  BYTEA        NOT NULL,
    -- NULL until the first request finished (a second one meanwhile gets 409 IDEMPOTENCY_IN_PROGRESS)
    status_code   SMALLINT,
    content_type  VARCHAR(100),
    response_body BYTEA,
    created_at    TIMESTAMPTZ  NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, key)
);

CREATE INDEX idempotency_keys_created_at_idx ON idempotency_keys (created_at);
