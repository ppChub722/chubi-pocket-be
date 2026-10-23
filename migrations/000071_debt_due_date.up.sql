-- 0.4.0-3 "กำหนดจ่าย": an optional due date on a debt entry (never on a repayment).
ALTER TABLE personal_debts ADD COLUMN due_date DATE;
