-- 0.4.1-2 multi-payer bills (owner 2026-10-28): a bill has one or more payers ("คนจ่าย", Σ = the bill)
-- and the shares ("คนหาร", Σ = the bill). Rows here ONLY when a bill has 2+ payers; none = the bill's
-- transaction_member_id paid it all (every bill before this). transaction_member_id = the main
-- (biggest) payer either way, so the share rule stays: child rows + the main payer's rest.
CREATE TABLE project_transaction_payers (
    project_transaction_id UUID NOT NULL REFERENCES project_transactions(id) ON DELETE CASCADE,
    member_id              UUID NOT NULL REFERENCES project_members(id) ON DELETE CASCADE,
    amount                 DECIMAL(15,2) NOT NULL CHECK (amount > 0),
    PRIMARY KEY (project_transaction_id, member_id)
);
CREATE INDEX idx_project_transaction_payers_member ON project_transaction_payers (member_id);

-- A completed event is locked for good (101): its payers can't change either.
CREATE OR REPLACE FUNCTION project_transaction_payers_locked_check() RETURNS TRIGGER AS $$
BEGIN
    IF EXISTS (SELECT 1 FROM project_transactions p JOIN projects pr ON pr.id = p.project_id
               WHERE p.id = COALESCE(NEW.project_transaction_id, OLD.project_transaction_id)
                 AND pr.completed_at IS NOT NULL FOR SHARE OF pr) THEN
        RAISE EXCEPTION 'EVENT_COMPLETED' USING ERRCODE = 'P0001', HINT = 'event_completed';
    END IF;
    RETURN COALESCE(NEW, OLD);
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER project_transaction_payers_locked
BEFORE INSERT OR UPDATE OR DELETE ON project_transaction_payers
FOR EACH ROW
EXECUTE FUNCTION project_transaction_payers_locked_check();

-- Every bill's real pairs. A share is split over the payers in proportion to what each paid:
-- member m owes payer p = share_m × paid_p / bill (2 dp). One payer = the child rows owe the payer, as
-- before. Income (Q-E3): the one who received owes each share (debtor / creditor flipped). A payback
-- (from → to): the receiver owes the giver, as before.
CREATE VIEW project_bill_pairs AS
WITH bills AS (
    SELECT p.id, p.project_id, p.type, p.amount, p.transaction_member_id
    FROM project_transactions p WHERE p.parent_project_transaction_id IS NULL
),
payers AS (
    SELECT b.id AS pt_id, x.member_id, x.amount AS paid
    FROM bills b JOIN project_transaction_payers x ON x.project_transaction_id = b.id
    UNION ALL
    SELECT b.id, b.transaction_member_id, b.amount
    FROM bills b
    WHERE NOT EXISTS (SELECT 1 FROM project_transaction_payers x WHERE x.project_transaction_id = b.id)
),
shares AS (
    SELECT c.parent_project_transaction_id AS pt_id, c.transaction_member_id AS member_id, SUM(c.amount) AS share
    FROM project_transactions c WHERE c.parent_project_transaction_id IS NOT NULL
    GROUP BY 1, 2
    UNION ALL
    SELECT b.id, b.transaction_member_id,
           b.amount - COALESCE((SELECT SUM(c.amount) FROM project_transactions c WHERE c.parent_project_transaction_id = b.id), 0)
    FROM bills b
)
SELECT b.id AS pt_id, b.project_id, b.type,
       CASE WHEN b.type = 'income' THEN pa.member_id ELSE s.member_id END AS debtor,
       CASE WHEN b.type = 'income' THEN s.member_id ELSE pa.member_id END AS creditor,
       ROUND(s.share * pa.paid / b.amount, 2) AS amount
FROM bills b
JOIN payers pa ON pa.pt_id = b.id
JOIN shares s ON s.pt_id = b.id
WHERE s.member_id <> pa.member_id AND s.share > 0 AND ROUND(s.share * pa.paid / b.amount, 2) > 0;
