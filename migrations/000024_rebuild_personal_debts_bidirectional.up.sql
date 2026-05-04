-- 1b refactor: unify shared_expense_splits + personal_debts into a single
-- bidirectional `personal_debts` table. Splits are no longer a separate
-- concept — every "X owes Y" relationship lives here, regardless of how
-- it originated (transaction split, manual cash loan, broken-item compensation).
--
-- See chubi-pocket-docs/design/spec/12-personal-debts.md (rewritten for
-- this phase) for the full model. Key changes:
--   - `personal_debts` becomes bidirectional via `direction` column
--     ('i_owe' | 'owed_to_me')
--   - Renames: creditor_* → counterparty_*, paid_amount → settled_amount
--   - New: source_transaction_id + source_project_transaction_id replace
--     source_split_id (which was on the now-dropped splits table)
--   - status='paid' renamed to 'settled' (direction-agnostic)

-- Drop downstream FK first (transactions.source_split_id → splits.id).
ALTER TABLE transactions DROP COLUMN IF EXISTS source_split_id;

-- Drop the splits table entirely.
DROP TABLE IF EXISTS shared_expense_splits CASCADE;

-- Drop the old personal_debts (test data only per migration plan).
DROP TABLE IF EXISTS personal_debts CASCADE;

-- New bidirectional personal_debts.
CREATE TABLE personal_debts (
    id                              UUID           PRIMARY KEY,
    user_id                         UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    direction                       VARCHAR(20)    NOT NULL
                                       CHECK (direction IN ('i_owe', 'owed_to_me')),
    -- Counterparty: the OTHER party in the obligation. Direction-agnostic
    -- naming. For 'i_owe' rows the counterparty is the creditor; for
    -- 'owed_to_me' rows the counterparty is the debtor.
    counterparty_contact_id         UUID           REFERENCES contacts(id) ON DELETE SET NULL,
    counterparty_person_name        VARCHAR(100)   NOT NULL,
    -- Origin transaction in the row owner's book. NULL for manual debts
    -- (cash loans not yet recorded, IOU agreements, broken-item compensation).
    -- For partner-side rows auto-created from a linked split, NULL because
    -- the partner has no transaction in their book yet (they didn't pay).
    source_transaction_id           UUID           REFERENCES transactions(id) ON DELETE SET NULL,
    -- 1b.2: project context for project-transaction splits.
    source_project_transaction_id   UUID           REFERENCES project_transactions(id) ON DELETE SET NULL,
    project_id                      UUID           REFERENCES projects(id) ON DELETE SET NULL,
    amount                          DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    -- How much has been settled (paid back / received back). Always 0..amount.
    -- Bumped automatically when a transaction with source_personal_debt_id
    -- lands; can also be edited directly (forgiveness, barter, no-cash adjust).
    settled_amount                  DECIMAL(15,2)  NOT NULL DEFAULT 0
                                       CHECK (settled_amount >= 0 AND settled_amount <= amount),
    currency                        VARCHAR(3)     NOT NULL,
    status                          VARCHAR(20)    NOT NULL DEFAULT 'open'
                                       CHECK (status IN ('open', 'settled', 'cancelled')),
    note                            TEXT,
    created_at                      TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id              UUID           REFERENCES users(id),
    updated_at                      TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id              UUID           REFERENCES users(id)
);

-- Primary list query: caller's debts filtered by status.
CREATE INDEX IF NOT EXISTS idx_personal_debts_user
    ON personal_debts(user_id, status);

-- People view: group by counterparty, net positions per person.
CREATE INDEX IF NOT EXISTS idx_personal_debts_counterparty_contact
    ON personal_debts(user_id, counterparty_contact_id)
    WHERE counterparty_contact_id IS NOT NULL;

-- "What debts came from this transaction" — used by FE detail view
-- when looking at a transaction with splits.
CREATE INDEX IF NOT EXISTS idx_personal_debts_source_tx
    ON personal_debts(source_transaction_id)
    WHERE source_transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_personal_debts_source_pt
    ON personal_debts(source_project_transaction_id)
    WHERE source_project_transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_personal_debts_project
    ON personal_debts(project_id)
    WHERE project_id IS NOT NULL;

CREATE TRIGGER set_timestamp_personal_debts
BEFORE UPDATE ON personal_debts
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
