-- Phase 1a `transactions` table. Omits 4 columns vs design/database/schema.md
-- §04 — they're added in 1b/1c when their FK targets exist:
--   project_id                     (1b — FK projects)
--   source_split_id                (1b — FK shared_expense_splits)
--   source_project_transaction_id  (1b — FK project_transactions)
--   scheduled_transaction_id       (1c — FK scheduled_transactions)
-- See product/phase1a/overview.md §Schema columns deferred.

CREATE TABLE IF NOT EXISTS transactions (
    id                  UUID           PRIMARY KEY,
    user_id             UUID           NOT NULL REFERENCES users(id),
    -- No ON DELETE CASCADE on account_id. Accounts are archive-only via the
    -- accounts handler; FK protects against orphaning if anything tries hard
    -- delete (DB rejects with FK violation).
    account_id          UUID           NOT NULL REFERENCES accounts(id),
    type                VARCHAR(10)    NOT NULL
                          CHECK (type IN ('expense', 'income', 'transfer')),
    amount              DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    -- category_id is required for transfers (system Transfer IN/OUT) and
    -- optional for expense/income (NULL = uncategorized). ON DELETE SET NULL
    -- so hard-deletion of a category from archived state with 0 references
    -- doesn't break this row (category permanent-delete checks tx count first).
    category_id         UUID           REFERENCES categories(id) ON DELETE SET NULL,
    date                DATE           NOT NULL,
    note                TEXT,
    -- transfer_group_id pairs the two rows of a transfer. NULL for non-transfers.
    -- Required (API-enforced) when type='transfer'.
    transfer_group_id   UUID,
    created_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id  UUID           REFERENCES users(id),
    updated_at          TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id  UUID           REFERENCES users(id)
);

CREATE INDEX IF NOT EXISTS idx_transactions_user_date
    ON transactions(user_id, date DESC, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_transactions_account_date
    ON transactions(account_id, date DESC, created_at DESC);

CREATE INDEX IF NOT EXISTS idx_transactions_category
    ON transactions(category_id, date DESC)
    WHERE category_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_transactions_transfer_group
    ON transactions(transfer_group_id)
    WHERE transfer_group_id IS NOT NULL;

CREATE TRIGGER set_timestamp_transactions
BEFORE UPDATE ON transactions
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
