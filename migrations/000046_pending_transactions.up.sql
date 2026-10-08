-- Pending transactions (owner design 2026-10-08): drafts waiting to be
-- confirmed. Not transactions yet — they never touch balances or reports.
-- Every field of the draft may be empty; the full transaction rules apply
-- only on submit. Today drafts are typed in by hand; notifications, OCR and
-- chat will add their own `source` values later without schema changes.
CREATE TABLE IF NOT EXISTS pending_transactions (
    id                     UUID         PRIMARY KEY,
    user_id                UUID         NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    -- Where the draft came from: manual | split_paid | project_copy |
    -- project_update | ocr | chat (free text so new sources need no migration).
    source                 VARCHAR(32)  NOT NULL DEFAULT 'manual',
    -- What submitting does:
    --   create      → a new transaction
    --   settle_debt → a new transaction that settles target_debt_id
    --   update_tx   → edits target_transaction_id
    kind                   VARCHAR(16)  NOT NULL DEFAULT 'create'
                             CHECK (kind IN ('create', 'settle_debt', 'update_tx')),
    -- The POST /transactions body (plus tag_ids), every key optional.
    draft                  JSONB        NOT NULL DEFAULT '{}'::jsonb,
    target_debt_id         UUID         REFERENCES personal_debts(id) ON DELETE CASCADE,
    target_transaction_id  UUID         REFERENCES transactions(id) ON DELETE CASCADE,
    -- What the draft is based on, for display (e.g. "Aom paid back ฿300",
    -- a scanned receipt). Never read by submit.
    source_ref             JSONB,
    -- Why the last submit failed (code + message); cleared on edit.
    last_error             JSONB,
    created_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at             TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    CONSTRAINT pending_kind_target CHECK (
        (kind = 'create'      AND target_debt_id IS NULL AND target_transaction_id IS NULL) OR
        (kind = 'settle_debt' AND target_debt_id IS NOT NULL) OR
        (kind = 'update_tx'   AND target_transaction_id IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_pending_transactions_user
    ON pending_transactions(user_id, created_at DESC);

CREATE TRIGGER set_timestamp_pending_transactions
BEFORE UPDATE ON pending_transactions
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
