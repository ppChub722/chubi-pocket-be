-- ถอนเงิน / เพิ่มเงิน (owner 2026-11-04): a transfer with one side only is one row in its own
-- system category, like an adjustment. New users get them from the seed; this adds them for
-- everyone already here.
INSERT INTO categories
    (id, user_id, name, type, parent_id, is_system, system_kind, icon_code, sort_order,
     include_in_report, description, created_by_user_id)
SELECT gen_random_uuid(), u.id, s.name, s.type, NULL, TRUE, s.kind,
       jsonb_build_object('icon', s.icon, 'iconColors', jsonb_build_array('#FFFFFF'),
                          'background', 'solid', 'bgColors', jsonb_build_array('#4DD0E1'),
                          'border', NULL, 'borderColors', '[]'::jsonb),
       COALESCE((SELECT MAX(c.sort_order) + 1 FROM categories c
                 WHERE c.user_id = u.id AND c.is_system AND c.type = s.type), 0),
       FALSE, NULL, NULL
FROM users u
CROSS JOIN (VALUES ('Withdrawal', 'expense', 'WITHDRAW', 'system_withdraw'),
                   ('Deposit', 'income', 'DEPOSIT', 'system_deposit')) AS s(name, type, kind, icon)
WHERE NOT EXISTS (SELECT 1 FROM categories c WHERE c.user_id = u.id AND c.system_kind = s.kind);
