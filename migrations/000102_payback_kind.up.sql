-- 0.4.1-2 V11: the debt-type bill = today's payback, widened. A payback row is money from one member to
-- another during the event; payback_kind says what it was: 'payback' (paying back what was owed, NULL
-- reads the same) or 'loan' (lending). The settle math is the same for both (the receiver owes the
-- giver); a claim books a payback as a repayment row and a loan as a debt row. Only on payback parents.
ALTER TABLE project_transactions ADD COLUMN payback_kind VARCHAR(10)
    CHECK (payback_kind IN ('payback', 'loan'));
