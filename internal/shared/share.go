package shared

import "fmt"

func EventBillPredicate(txAlias string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM project_transactions ep
		WHERE ep.linked_transaction_id = %[1]s.id AND ep.parent_project_transaction_id IS NULL)`, txAlias)
}

func ShareAmountExpr(txAlias string) string {
	return fmt.Sprintf(`GREATEST(%[1]s.amount
		- COALESCE((SELECT SUM(sd.amount) FROM personal_debts sd
			WHERE sd.source_transaction_id = %[1]s.id AND sd.user_id = %[1]s.user_id), 0)
		- CASE WHEN %[2]s THEN COALESCE((SELECT SUM(sc.amount) FROM project_transactions sc
			JOIN project_transactions sp ON sp.id = sc.parent_project_transaction_id
			WHERE sp.linked_transaction_id = %[1]s.id), 0)
		  ELSE 0 END, 0)`, txAlias, EventBillPredicate(txAlias))
}
