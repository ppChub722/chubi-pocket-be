-- 0.4.2-4 split phase A: the real pairs of every bill, read from the people table
-- (replaces project_bill_pairs, which reads the child rows + payers; that one stays until
-- migration 126 removes the old tables).
-- On a bill, a person who paid owes nothing; every other person owes each payer
-- share x paid / amount (2 dp). Income is flipped (the one who received owes each share).
-- A payback (from -> to): the receiver owes the giver, as an expense does.
-- debtor / creditor are event members (NULL on a bill with no event), *_person the people rows.
CREATE VIEW bill_pairs AS
SELECT b.id AS pt_id, b.project_id, b.type,
       CASE WHEN b.type = 'income' THEN pa.event_member_id ELSE s.event_member_id END AS debtor,
       CASE WHEN b.type = 'income' THEN s.event_member_id ELSE pa.event_member_id END AS creditor,
       CASE WHEN b.type = 'income' THEN pa.id ELSE s.id END AS debtor_person,
       CASE WHEN b.type = 'income' THEN s.id ELSE pa.id END AS creditor_person,
       ROUND(s.share * pa.paid / b.amount, 2) AS amount
FROM project_transactions b
JOIN split_transaction_people pa ON pa.split_transaction_id = b.id AND pa.paid > 0
JOIN split_transaction_people s  ON s.split_transaction_id = b.id AND s.share > 0 AND s.id <> pa.id
WHERE b.parent_project_transaction_id IS NULL
  AND ROUND(s.share * pa.paid / b.amount, 2) > 0;
