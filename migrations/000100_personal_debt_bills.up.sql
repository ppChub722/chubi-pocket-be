-- 0.4.1-2 V5 / S10: the copy ⇄ debt link. A debt can cover many bills (a lump, "claim ก้อน"); a
-- per-bill debt is a lump of one. One row = one bill's part of one debt, with the other person of
-- that bill (member_id) and my transaction of that bill (the "จ่ายโดย X" copy, or my origin when I
-- paid). debt.amount = Σ its parts (kept by the writer). One bill + person is booked once per user
-- (X7). Only kind = 'debt' rows: paybacks are claimed as repayment rows and keep
-- personal_debts.source_project_transaction_id (091). During 0.4.1-2 per-bill debts keep that column
-- set as well; it is dropped for debts once nothing reads it.
CREATE TABLE personal_debt_bills (
    id             UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    debt_id        UUID NOT NULL REFERENCES personal_debts(id) ON DELETE CASCADE,
    user_id        UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    pt_id          UUID NOT NULL REFERENCES project_transactions(id) ON DELETE CASCADE,
    member_id      UUID NOT NULL REFERENCES project_members(id) ON DELETE CASCADE,
    transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL,
    amount         DECIMAL(15,2) NOT NULL CHECK (amount >= 0),
    created_at     TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_personal_debt_bills_pair ON personal_debt_bills (user_id, pt_id, member_id);
CREATE INDEX idx_personal_debt_bills_debt ON personal_debt_bills (debt_id);
CREATE INDEX idx_personal_debt_bills_pt ON personal_debt_bills (pt_id);
CREATE INDEX idx_personal_debt_bills_tx ON personal_debt_bills (transaction_id) WHERE transaction_id IS NOT NULL;

CREATE TRIGGER set_timestamp_personal_debt_bills
BEFORE UPDATE ON personal_debt_bills
FOR EACH ROW
EXECUTE FUNCTION set_timestamp();

-- Backfill: every per-bill debt becomes a lump of one. The bill = the parent row the debt points at
-- (or the parent of a split row); the member = the bill's event member who is the debt's person
-- (linked contact → user, else the name); my tx = my origin / copy of that bill (not a debt's own
-- tx). Debts with no matching member, and a second debt on the same bill + person (X7), are left
-- out (they stay as they are).
INSERT INTO personal_debt_bills (debt_id, user_id, pt_id, member_id, transaction_id, amount)
SELECT DISTINCT ON (pd.user_id, bill.id, m.id)
       pd.id, pd.user_id, bill.id, m.id,
       (SELECT t.id FROM transactions t
         WHERE t.user_id = pd.user_id AND t.source_personal_debt_id IS NULL
           AND (t.id = bill.linked_transaction_id OR t.source_project_transaction_id = bill.id
                OR t.source_project_transaction_id IN (
                    SELECT c.id FROM project_transactions c WHERE c.parent_project_transaction_id = bill.id))
         ORDER BY (t.id = bill.linked_transaction_id) DESC, t.created_at LIMIT 1),
       pd.amount
FROM personal_debts pd
JOIN project_transactions src ON src.id = pd.source_project_transaction_id
JOIN project_transactions bill ON bill.id = COALESCE(src.parent_project_transaction_id, src.id)
JOIN LATERAL (
    SELECT pm.id FROM project_members pm
    LEFT JOIN users u ON u.id = pm.user_id
    LEFT JOIN contacts c ON c.id = pd.counterparty_contact_id
    WHERE pm.project_id = bill.project_id
      AND pm.user_id IS DISTINCT FROM pd.user_id
      AND ((pm.user_id IS NOT NULL AND pm.user_id = c.linked_user_id)
        OR LOWER(BTRIM(COALESCE(u.display_name, pm.display_name))) = LOWER(BTRIM(pd.counterparty_person_name))
        OR LOWER(BTRIM(pm.display_name)) = LOWER(BTRIM(pd.counterparty_person_name)))
    ORDER BY (pm.user_id IS NOT NULL AND pm.user_id = c.linked_user_id) DESC, pm.created_at
    LIMIT 1) m ON TRUE
WHERE pd.kind = 'debt' AND bill.type <> 'payback'
ORDER BY pd.user_id, bill.id, m.id, pd.created_at
ON CONFLICT DO NOTHING;
