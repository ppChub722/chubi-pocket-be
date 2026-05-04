-- 1b.1 ships personal-context personal_debts only. Column deferred to 1b.2:
--   project_id  (FK projects; contextual project)
-- See design/spec/12-personal-debts.md §1 + product/phase1b/db.md migration 15.

CREATE TABLE IF NOT EXISTS personal_debts (
    id                     UUID           PRIMARY KEY,
    user_id                UUID           NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    creditor_contact_id    UUID           REFERENCES contacts(id) ON DELETE SET NULL,
    -- creditor_person_name is ALWAYS set — the durable display label.
    -- When creditor_contact_id is NULL, this IS the creditor identifier.
    creditor_person_name   VARCHAR(100)   NOT NULL,
    -- ON DELETE SET NULL: if the source split is deleted, the debt becomes
    -- "unlinked" but the row survives — protects the debtor from data loss
    -- when the creditor mutates their book. See spec §4.6.
    source_split_id        UUID           REFERENCES shared_expense_splits(id) ON DELETE SET NULL,
    amount                 DECIMAL(15,2)  NOT NULL CHECK (amount > 0),
    paid_amount            DECIMAL(15,2)  NOT NULL DEFAULT 0
                             CHECK (paid_amount >= 0 AND paid_amount <= amount),
    currency               VARCHAR(3)     NOT NULL,
    status                 VARCHAR(20)    NOT NULL DEFAULT 'open'
                             CHECK (status IN ('open', 'paid', 'cancelled')),
    note                   TEXT,
    created_at             TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    created_by_user_id     UUID           REFERENCES users(id),
    updated_at             TIMESTAMPTZ    NOT NULL DEFAULT NOW(),
    updated_by_user_id     UUID           REFERENCES users(id)
);

-- Prevent double-tracking the same split. Only one personal_debts row per
-- (user, source_split_id). NULL source_split_id rows are unconstrained.
CREATE UNIQUE INDEX IF NOT EXISTS idx_personal_debts_source
    ON personal_debts(user_id, source_split_id)
    WHERE source_split_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_personal_debts_user
    ON personal_debts(user_id, status);

CREATE INDEX IF NOT EXISTS idx_personal_debts_creditor_contact
    ON personal_debts(creditor_contact_id)
    WHERE creditor_contact_id IS NOT NULL;

CREATE TRIGGER set_timestamp_personal_debts
BEFORE UPDATE ON personal_debts
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();
