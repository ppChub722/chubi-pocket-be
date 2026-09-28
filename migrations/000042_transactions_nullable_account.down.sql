UPDATE transactions SET account_id = (SELECT id FROM accounts WHERE user_id = transactions.user_id LIMIT 1)
WHERE account_id IS NULL;

ALTER TABLE transactions ALTER COLUMN account_id SET NOT NULL;
