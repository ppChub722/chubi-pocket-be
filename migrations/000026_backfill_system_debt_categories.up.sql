-- Two new system categories auto-assigned on personal_debts settlement.
-- Backfill rows for every existing user. New registrations get them via
-- the auth registration hook (categories.SeedForUser).
--
-- Both have include_in_report=false so debt repayments don't pollute
-- spending reports — the underlying transaction amount that created the
-- debt was already accounted for.

INSERT INTO categories
    (id, user_id, name, type, is_system, system_kind, icon, color,
     include_in_report, created_by_user_id, updated_by_user_id)
SELECT
    gen_random_uuid(),
    u.id,
    'Debt Received',
    'income',
    TRUE,
    'DEBT_RECEIVED',
    'system_debt_received',
    '#AED581',
    FALSE,
    u.id,
    u.id
FROM users u
ON CONFLICT (user_id, system_kind) WHERE system_kind IS NOT NULL DO NOTHING;

INSERT INTO categories
    (id, user_id, name, type, is_system, system_kind, icon, color,
     include_in_report, created_by_user_id, updated_by_user_id)
SELECT
    gen_random_uuid(),
    u.id,
    'Debt Paid',
    'expense',
    TRUE,
    'DEBT_PAID',
    'system_debt_paid',
    '#E57373',
    FALSE,
    u.id,
    u.id
FROM users u
ON CONFLICT (user_id, system_kind) WHERE system_kind IS NOT NULL DO NOTHING;

-- Backfill existing settlement transactions that landed without a
-- category (created before this fix). Match by source_personal_debt_id
-- + transaction type, then look up that user's matching system category.
UPDATE transactions t
SET category_id = c.id, updated_by_user_id = t.user_id
FROM categories c, personal_debts pd
WHERE t.category_id IS NULL
  AND t.source_personal_debt_id = pd.id
  AND c.user_id = t.user_id
  AND c.is_system = TRUE
  AND c.system_kind = CASE
        WHEN t.type = 'income'  THEN 'DEBT_RECEIVED'
        WHEN t.type = 'expense' THEN 'DEBT_PAID'
      END;
