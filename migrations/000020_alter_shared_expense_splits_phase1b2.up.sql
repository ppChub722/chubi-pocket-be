-- 1b.2: complete shared_expense_splits to its full schema. Until now the
-- table was personal-context-only (NOT NULL on source_transaction_id, no
-- project columns).
-- See design/spec/06-shared-expenses.md §1, §2.5.

ALTER TABLE shared_expense_splits
    ADD COLUMN IF NOT EXISTS source_project_transaction_id UUID
        REFERENCES project_transactions(id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS project_member_id UUID
        REFERENCES project_members(id) ON DELETE CASCADE;

-- Drop the 1b.1 NOT NULL on source_transaction_id so project-context splits
-- can leave it null and use source_project_transaction_id instead.
ALTER TABLE shared_expense_splits
    ALTER COLUMN source_transaction_id DROP NOT NULL;

-- Replace the implicit single-source rule with the structural XOR check:
-- exactly one of (source_transaction_id, source_project_transaction_id) set.
ALTER TABLE shared_expense_splits
    ADD CONSTRAINT splits_source_xor CHECK (
        (source_transaction_id IS NOT NULL)::int +
        (source_project_transaction_id IS NOT NULL)::int = 1
    );

CREATE INDEX IF NOT EXISTS idx_splits_source_pt
    ON shared_expense_splits(source_project_transaction_id)
    WHERE source_project_transaction_id IS NOT NULL;

CREATE INDEX IF NOT EXISTS idx_splits_project_member
    ON shared_expense_splits(project_member_id)
    WHERE project_member_id IS NOT NULL;
