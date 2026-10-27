UPDATE transactions t
SET category_id = d.id
FROM categories c, categories d
WHERE t.category_id = c.id
  AND c.system_kind IN ('LOAN_GIVEN', 'LOAN_TAKEN')
  AND d.user_id = t.user_id
  AND d.system_kind = CASE c.system_kind WHEN 'LOAN_GIVEN' THEN 'DEBT_PAID' ELSE 'DEBT_RECEIVED' END;

DELETE FROM categories WHERE system_kind IN ('LOAN_GIVEN', 'LOAN_TAKEN');
