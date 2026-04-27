CREATE TABLE IF NOT EXISTS accounts (
    id                  UUID           PRIMARY KEY,
    user_id             UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    name                VARCHAR(100)   NOT NULL,
    type                VARCHAR(20)    NOT NULL
                          CHECK (type IN ('cash', 'bank', 'e_wallet', 'credit_card', 'pay_later')),
    -- balance is a CACHED sum of transactions for this account.
    -- Maintained atomically by the transaction service inside the same DB tx
    -- as every transaction insert/update/delete. NEVER updated via any other
    -- code path. PUT /v1/accounts/:id rejects `balance` in the request.
    -- See design/spec/03-accounts.md §3.2 and product/phase1a/db.md.
    balance             DECIMAL(15,2)  NOT NULL DEFAULT 0,
    currency            VARCHAR(3)     NOT NULL,
    icon                VARCHAR(50),
    color               VARCHAR(7),
    status              VARCHAR(20)    NOT NULL DEFAULT 'active'
                          CHECK (status IN ('active', 'archived', 'closed')),
    -- Credit-account fields. Required only when type IN ('credit_card', 'pay_later').
    -- API layer enforces presence/absence; DB only ranges on dates.
    credit_limit        DECIMAL(15,2),
    statement_date      SMALLINT       CHECK (statement_date IS NULL OR statement_date BETWEEN 1 AND 31),
    payment_due_date    SMALLINT       CHECK (payment_due_date IS NULL OR payment_due_date BETWEEN 1 AND 31),
    minimum_payment     DECIMAL(15,2),
    sort_order          INTEGER        NOT NULL DEFAULT 0,
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_accounts_user
    ON accounts(user_id, status);

CREATE INDEX IF NOT EXISTS idx_accounts_user_sort
    ON accounts(user_id, sort_order);

CREATE TRIGGER set_timestamp_accounts
BEFORE UPDATE ON accounts
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
