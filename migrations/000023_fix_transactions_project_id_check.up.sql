-- Migration 22 added a biconditional CHECK on transactions:
--   `project_id IS NOT NULL` = source set
-- That's too strict. A creditor recording receipt on a personal-context
-- split (no project) gets `source_split_id` set + `project_id = NULL`,
-- which violates the biconditional and 500s the resolve endpoint.
--
-- Spec §04/§3.1 (verbatim): "project_id auto-set from split's parent's
-- project_id (or NULL if parent is non-project personal transaction)".
-- The correct rule is a one-way implication: if project_id IS NOT NULL,
-- then a source MUST be set (so project_id can never appear standalone).
-- A source CAN exist without project_id (the personal-context resolve case).

ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_project_id_iff_source;

ALTER TABLE transactions
    ADD CONSTRAINT transactions_project_id_implies_source CHECK (
        project_id IS NULL
        OR source_split_id IS NOT NULL
        OR source_project_transaction_id IS NOT NULL
    );
