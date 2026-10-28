DROP VIEW IF EXISTS project_bill_pairs;
DROP TRIGGER IF EXISTS project_transaction_payers_locked ON project_transaction_payers;
DROP FUNCTION IF EXISTS project_transaction_payers_locked_check();
DROP TABLE IF EXISTS project_transaction_payers;
