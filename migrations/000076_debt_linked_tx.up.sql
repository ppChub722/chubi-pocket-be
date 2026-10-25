-- 0.4.0-4 "บันทึกในรายการ": a debt entry may point at one of my existing transactions.
-- Unlike the entry's own transaction (transactions.source_personal_debt_id) it is a soft link:
-- the transaction keeps its category, unlinking keeps it, deleting it only clears the link.
ALTER TABLE personal_debts
    ADD COLUMN linked_transaction_id UUID REFERENCES transactions(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX personal_debts_linked_transaction_uniq
    ON personal_debts (linked_transaction_id) WHERE linked_transaction_id IS NOT NULL;
