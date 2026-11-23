-- 0.4.3: ดอกเบี้ย. Paying a plan's installment books its interest as an expense (INTEREST_PAID),
-- collecting one as an income (INTEREST_RECEIVED); both count in reports. New users get them from
-- the seed; this adds them for everyone already here.
INSERT INTO categories
    (id, user_id, name, type, parent_id, is_system, system_kind, icon_code, sort_order,
     include_in_report, description, created_by_user_id)
SELECT gen_random_uuid(), u.id, s.name, s.type, NULL, TRUE, s.kind,
       jsonb_build_object('icon', s.icon, 'iconColors', jsonb_build_array('#FFFFFF'),
                          'background', 'solid', 'bgColors', jsonb_build_array(s.color),
                          'border', NULL, 'borderColors', '[]'::jsonb),
       COALESCE((SELECT MAX(c.sort_order) + 1 FROM categories c
                 WHERE c.user_id = u.id AND c.is_system AND c.type = s.type), 0),
       TRUE, NULL, NULL
FROM users u
CROSS JOIN (VALUES ('Interest Paid', 'expense', 'INTEREST_PAID', 'percent', '#E57373'),
                   ('Interest Received', 'income', 'INTEREST_RECEIVED', 'percent', '#AED581')) AS s(name, type, kind, icon, color)
WHERE NOT EXISTS (SELECT 1 FROM categories c WHERE c.user_id = u.id AND c.system_kind = s.kind);
