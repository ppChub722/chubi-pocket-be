-- 0.4.1-1 D5: four person-book system categories, one per kind:
--   LOAN_GIVEN    (ให้กู้ยืม, expense) — a debt I paid (lend)
--   LOAN_TAKEN    (กู้ยืม, income)    — a debt they paid (borrow)
--   DEBT_PAID     (ชำระหนี้, expense)  — a repayment I pay
--   DEBT_RECEIVED (รับชำระหนี้, income) — a repayment I get
-- The app names them from system_kind. All out of reports. New users get them from
-- categories.SeedForUser.

INSERT INTO categories
    (id, user_id, name, type, parent_id, is_system, system_kind, icon_code, sort_order,
     include_in_report, created_by_user_id)
SELECT gen_random_uuid(), u.id, v.name, v.type, NULL, TRUE, v.kind,
       jsonb_build_object('icon', v.icon, 'iconColors', jsonb_build_array('#FFFFFF'),
                          'background', 'solid', 'bgColors', jsonb_build_array(v.color), 'border', NULL,
                          'borderColors', jsonb_build_array()),
       COALESCE((SELECT MAX(c.sort_order) + 1 FROM categories c
                 WHERE c.user_id = u.id AND c.parent_id IS NULL AND c.type = v.type), 0),
       FALSE, NULL
FROM users u
CROSS JOIN (VALUES
    ('Loan Given', 'expense', 'LOAN_GIVEN', 'volunteer_activism', '#E57373'),
    ('Loan Taken', 'income', 'LOAN_TAKEN', 'handshake', '#AED581')
) AS v(name, type, kind, icon, color)
ON CONFLICT (user_id, system_kind) WHERE system_kind IS NOT NULL DO NOTHING;

-- Existing DEBT transactions (a debt's own transaction) move to the loan categories; repayment
-- transactions (081) stay in DEBT_PAID / DEBT_RECEIVED.
UPDATE transactions t
SET category_id = c.id
FROM personal_debts pd, categories c
WHERE t.source_personal_debt_id = pd.id
  AND pd.kind = 'debt'
  AND c.user_id = t.user_id
  AND c.system_kind = CASE t.type WHEN 'expense' THEN 'LOAN_GIVEN' ELSE 'LOAN_TAKEN' END;
