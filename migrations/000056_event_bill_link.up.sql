-- 0.3.1-10: a bill made from a personal transaction keeps an EXPLICIT link
-- (its money is linked by prompt now, so the amounts may differ — the old
-- "same payer + same amount" test can't find it any more). The link lives on
-- the module's own record; a deleted transaction just breaks it. The
-- recurring items (migration 057) use the same mechanism.

ALTER TABLE project_transactions
    ADD COLUMN linked_transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL;

UPDATE project_transactions ep SET linked_transaction_id = t.id
FROM transactions t, project_members epm
WHERE t.source_project_transaction_id = ep.id
  AND ep.parent_project_transaction_id IS NULL
  AND epm.id = ep.transaction_member_id
  AND epm.user_id = t.user_id
  AND ep.amount = t.amount;

CREATE UNIQUE INDEX idx_project_transactions_linked_tx
    ON project_transactions (linked_transaction_id) WHERE linked_transaction_id IS NOT NULL;
