-- 1c — scheduled_transactions table + transactions.scheduled_transaction_id FK.
-- Spec: design/spec/11-scheduled-transactions.md
-- Schema: design/database/schema.md §11 — Scheduled Transactions
--
-- `day_of_month` is captured at create time so monthly schedules with
-- day=29/30/31 can clamp to last-day-of-month on advance without
-- losing the original intent (spec §4.4). Schema doc updated to match.

CREATE TABLE IF NOT EXISTS scheduled_transactions (
    id                       UUID           PRIMARY KEY,
    user_id                  UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id               UUID           NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    name                     VARCHAR(100)   NOT NULL,
    type                     VARCHAR(10)    NOT NULL
                               CHECK (type IN ('expense', 'income')),
    entry_type               VARCHAR(20)    NOT NULL
                               CHECK (entry_type IN ('recurring', 'installment')),
    amount                   DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    category_id              UUID           REFERENCES categories(id) ON DELETE SET NULL,
    billing_cycle            VARCHAR(20)    NOT NULL
                               CHECK (billing_cycle IN ('daily', 'weekly', 'monthly', 'yearly')),
    next_billing_date        DATE           NOT NULL,
    -- Original intended day-of-month (for monthly), 1..31. Captured at
    -- create time from `next_billing_date`. Used by the advance helper
    -- to clamp to min(day_of_month, days_in_target_month) — preserves
    -- "last day of month" intent across short months.
    day_of_month             SMALLINT       NOT NULL DEFAULT 1
                               CHECK (day_of_month BETWEEN 1 AND 31),
    status                   VARCHAR(20)    NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active', 'paused', 'cancelled', 'completed')),
    note                     TEXT,
    -- Installment-only fields. NULL when entry_type='recurring' (API-enforced).
    total_amount             DECIMAL(15,2),
    down_payment             DECIMAL(15,2),
    total_installments       INTEGER        CHECK (total_installments IS NULL OR total_installments > 0),
    remaining_installments   INTEGER        CHECK (remaining_installments IS NULL OR remaining_installments >= 0),
    interest_rate            DECIMAL(5,2),
    icon_code                JSONB,
    logo_url                 TEXT,
    created_at               TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id       UUID           REFERENCES users(id),
    updated_at               TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id       UUID           REFERENCES users(id)
);

-- Caller's active schedules — primary list query.
CREATE INDEX IF NOT EXISTS idx_scheduled_transactions_user
    ON scheduled_transactions(user_id, status);

-- Phase 3 scheduler's "due today" sweep. Partial — only active rows
-- ever fire, so we don't pay for paused/cancelled/completed rows.
CREATE INDEX IF NOT EXISTS idx_scheduled_transactions_next_billing
    ON scheduled_transactions(next_billing_date, status)
    WHERE status = 'active';

CREATE INDEX IF NOT EXISTS idx_scheduled_transactions_account
    ON scheduled_transactions(account_id, status);

CREATE TRIGGER set_timestamp_scheduled_transactions
BEFORE UPDATE ON scheduled_transactions
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();

-- ── Wire transactions.scheduled_transaction_id ────────────────────────────
-- Generated rows reference back to the originating schedule. ON DELETE
-- SET NULL so deleting a schedule doesn't cascade-destroy historical
-- transactions (spec §3.6 — generated transactions outlive the schedule).

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS scheduled_transaction_id UUID
        REFERENCES scheduled_transactions(id) ON DELETE SET NULL;

CREATE INDEX IF NOT EXISTS idx_transactions_scheduled
    ON transactions(scheduled_transaction_id)
    WHERE scheduled_transaction_id IS NOT NULL;
