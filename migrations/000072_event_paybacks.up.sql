-- 0.4.0-3 event book: a payback ("A paid B") is a row of its own.
-- Parent: type 'payback', transaction_member_id = who paid back. One child: who got it, the same amount.
ALTER TABLE project_transactions DROP CONSTRAINT IF EXISTS project_transactions_type_check;
ALTER TABLE project_transactions ADD CONSTRAINT project_transactions_type_check
    CHECK (type IN ('expense', 'income', 'payback'));
