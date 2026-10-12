-- Best effort: recurring data is lost; the old (empty) scheduled table comes back.

ALTER TABLE notifications DROP CONSTRAINT notifications_type_check;
DELETE FROM notifications WHERE type IN ('recurring_due', 'recurring_ending');
ALTER TABLE notifications ADD CONSTRAINT notifications_type_check CHECK (type IN (
    'split_created', 'split_paid', 'split_received', 'split_changed',
    'project_tx_recorded_for_you', 'project_tx_changed', 'project_invite',
    'contact_link_request', 'account_invite', 'project_added'));

DROP TABLE IF EXISTS recurring_items;
DROP TABLE IF EXISTS recurring_rules;

CREATE TABLE IF NOT EXISTS scheduled_transactions (
    id                       UUID           PRIMARY KEY,
    user_id                  UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    account_id               UUID           NOT NULL REFERENCES accounts(id) ON DELETE RESTRICT,
    name                     VARCHAR(100)   NOT NULL,
    type                     VARCHAR(10)    NOT NULL CHECK (type IN ('expense', 'income')),
    entry_type               VARCHAR(20)    NOT NULL CHECK (entry_type IN ('recurring', 'installment')),
    amount                   DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    category_id              UUID           REFERENCES categories(id) ON DELETE SET NULL,
    billing_cycle            VARCHAR(20)    NOT NULL CHECK (billing_cycle IN ('daily', 'weekly', 'monthly', 'yearly')),
    next_billing_date        DATE           NOT NULL,
    day_of_month             SMALLINT       NOT NULL DEFAULT 1 CHECK (day_of_month BETWEEN 1 AND 31),
    status                   VARCHAR(20)    NOT NULL DEFAULT 'active'
                               CHECK (status IN ('active', 'paused', 'cancelled', 'completed')),
    note                     TEXT,
    description              TEXT,
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
ALTER TABLE transactions ADD COLUMN IF NOT EXISTS scheduled_transaction_id UUID
    REFERENCES scheduled_transactions(id) ON DELETE SET NULL;
