-- 0.4.1-2 K1 (BE-3): the recurring "หนี้" kind. A debt setting is tied to a person (a contact or a
-- typed name) and books PERSON-BOOK entries (personal_debts): debt_kind debt = จดหนี้, repayment =
-- จ่ายหนี้; debt_direction paid | received as in the debts API. A slot is filled by its entry
-- (recurring_items.debt_entry_id). A จ่ายหนี้ setting pauses with pause_reason no_balance after 3
-- rounds in a row with nothing owed (zero_rounds) and runs again on a new debt.
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_type_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_type_check
    CHECK (type IN ('expense', 'income', 'transfer', 'saving', 'debt'));
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_kind_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_kind_check
    CHECK (kind IN ('subscription', 'installment', 'income', 'reminder', 'saving', 'debt'));
ALTER TABLE recurring_rules DROP CONSTRAINT IF EXISTS recurring_rules_pause_reason_check;
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_pause_reason_check
    CHECK (pause_reason IN ('manual', 'overdue_limit', 'goal_inactive', 'no_balance'));

ALTER TABLE recurring_rules
    ADD COLUMN contact_id     UUID REFERENCES contacts(id) ON DELETE SET NULL,
    ADD COLUMN person_name    VARCHAR(100),
    ADD COLUMN debt_kind      VARCHAR(20) CHECK (debt_kind IN ('debt', 'repayment')),
    ADD COLUMN debt_direction VARCHAR(20) CHECK (debt_direction IN ('paid', 'received')),
    ADD COLUMN zero_rounds    INT NOT NULL DEFAULT 0 CHECK (zero_rounds BETWEEN 0 AND 3);

-- A debt setting always has its person (the name stays when the contact goes) and both choices;
-- other kinds have none of them.
ALTER TABLE recurring_rules ADD CONSTRAINT recurring_rules_debt_fields_check CHECK (
    (type = 'debt') = (person_name IS NOT NULL AND debt_kind IS NOT NULL AND debt_direction IS NOT NULL)
    AND (type = 'debt' OR contact_id IS NULL));

CREATE INDEX idx_recurring_rules_contact ON recurring_rules (contact_id) WHERE contact_id IS NOT NULL;

-- K6: deleting the entry only empties the slot (the code skips it with its last values first).
ALTER TABLE recurring_items
    ADD COLUMN debt_entry_id UUID REFERENCES personal_debts(id) ON DELETE SET NULL;
CREATE UNIQUE INDEX idx_recurring_items_debt_entry ON recurring_items (debt_entry_id) WHERE debt_entry_id IS NOT NULL;
