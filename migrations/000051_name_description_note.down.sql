UPDATE budgets SET description = LEFT(name, 200);
ALTER TABLE budgets ALTER COLUMN description TYPE VARCHAR(200);
ALTER TABLE budgets DROP COLUMN IF EXISTS name;

ALTER TABLE scheduled_transactions DROP COLUMN IF EXISTS description;
ALTER TABLE projects       DROP COLUMN IF EXISTS note;
ALTER TABLE saving_goals   DROP COLUMN IF EXISTS description;
ALTER TABLE personal_debts DROP COLUMN IF EXISTS description;
ALTER TABLE transactions   DROP COLUMN IF EXISTS description;

ALTER TABLE contacts DROP COLUMN IF EXISTS description;
ALTER TABLE contacts RENAME COLUMN note TO notes;

ALTER TABLE tags
    DROP COLUMN IF EXISTS description,
    DROP COLUMN IF EXISTS note;
