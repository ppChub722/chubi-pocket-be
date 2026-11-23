DELETE FROM categories WHERE is_system AND system_kind IN ('INTEREST_PAID', 'INTEREST_RECEIVED')
    AND NOT EXISTS (SELECT 1 FROM transactions t WHERE t.category_id = categories.id);
