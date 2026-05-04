ALTER TABLE transactions
    DROP CONSTRAINT IF EXISTS transactions_project_id_implies_source;

ALTER TABLE transactions
    ADD CONSTRAINT transactions_project_id_iff_source CHECK (
        (project_id IS NOT NULL) =
        (source_split_id IS NOT NULL OR source_project_transaction_id IS NOT NULL)
    );
