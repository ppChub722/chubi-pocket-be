-- Import logs (owner design 2026-10-09): written the moment slip import
-- meets something it can't handle yet — a bank code not in
-- payment_providers, a bank with no slip rule set, a slip whose amount /
-- date didn't read. No cron: we open it on our own schedule
-- (ops-runbook has the queries). The user's email etc. come from a join
-- on users when reading; the slip's text / image are never stored here.
CREATE TABLE IF NOT EXISTS import_logs (
    id          UUID         PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id     UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- unknown_provider | unsupported_bank | incomplete (free text: new
    -- kinds need no migration)
    kind        VARCHAR(32)  NOT NULL,
    scheme      VARCHAR(20),
    code        VARCHAR(20),
    trans_ref   VARCHAR(64),
    details     JSONB        NOT NULL DEFAULT '{}'::jsonb,
    created_at  TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS idx_import_logs_kind_created
    ON import_logs(kind, created_at DESC);
