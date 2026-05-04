-- 1b.2: complete the transactions schema. project_id is auto-derived from
-- the source (split's parent project, or claim source's project) — never
-- settable directly via POST /v1/transactions. source_project_transaction_id
-- is set only on rows created via the claim flow.
-- See design/spec/04-transactions.md §1, §3.1 + product/phase1b/db.md.

ALTER TABLE transactions
    ADD COLUMN IF NOT EXISTS project_id UUID
        REFERENCES projects(id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS source_project_transaction_id UUID
        REFERENCES project_transactions(id) ON DELETE SET NULL;

-- A transaction has at most one source signal (split-resolve XOR claim).
ALTER TABLE transactions
    ADD CONSTRAINT transactions_source_at_most_one CHECK (
        (source_split_id IS NOT NULL)::int +
        (source_project_transaction_id IS NOT NULL)::int <= 1
    );

-- project_id is set iff some source signal is present. Eliminates the
-- failure mode where project_id leaks onto a non-project transaction.
ALTER TABLE transactions
    ADD CONSTRAINT transactions_project_id_iff_source CHECK (
        (project_id IS NOT NULL) =
        (source_split_id IS NOT NULL OR source_project_transaction_id IS NOT NULL)
    );

CREATE INDEX IF NOT EXISTS idx_transactions_project
    ON transactions(project_id, date DESC)
    WHERE project_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_transactions_source_pt
    ON transactions(source_project_transaction_id)
    WHERE source_project_transaction_id IS NOT NULL;
