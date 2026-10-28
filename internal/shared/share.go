package shared

import "fmt"

func EventBillPredicate(txAlias string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM project_transactions ep
		WHERE ep.linked_transaction_id = %[1]s.id AND ep.parent_project_transaction_id IS NULL)`, txAlias)
}

// ShareAmountExpr — what a row counts in reports / budgets / list totals: my share. A copy of a
// shared bill (0.4.1-2, linked by source_project_transaction_id) counts LEAST(its amount, my share of
// the bill): the payer's whole bill and a multi-payer copy holding what I paid both count my share; a
// smaller copy left out of sync counts its own amount (my own splits of it still come off). Else: the amount minus my splits (and an
// event bill made from it, its other shares).
func ShareAmountExpr(txAlias string) string {
	return fmt.Sprintf(`COALESCE(
		(SELECT GREATEST(LEAST(%[1]s.amount, CASE WHEN bb.transaction_member_id = bm.id
				THEN bb.amount - COALESCE((SELECT SUM(bc.amount) FROM project_transactions bc
					WHERE bc.parent_project_transaction_id = bb.id), 0)
				ELSE COALESCE((SELECT SUM(bc.amount) FROM project_transactions bc
					WHERE bc.parent_project_transaction_id = bb.id AND bc.transaction_member_id = bm.id), 0) END)
			- COALESCE((SELECT SUM(sd.amount) FROM personal_debts sd
				WHERE sd.source_transaction_id = %[1]s.id AND sd.user_id = %[1]s.user_id), 0), 0)
		 FROM project_transactions bs
		 JOIN project_transactions bb ON bb.id = COALESCE(bs.parent_project_transaction_id, bs.id) AND bb.type <> 'payback'
		 JOIN project_members bm ON bm.project_id = bb.project_id AND bm.user_id = %[1]s.user_id
		 WHERE bs.id = %[1]s.source_project_transaction_id),
		GREATEST(%[1]s.amount
		- COALESCE((SELECT SUM(sd.amount) FROM personal_debts sd
			WHERE sd.source_transaction_id = %[1]s.id AND sd.user_id = %[1]s.user_id), 0)
		- CASE WHEN %[2]s THEN COALESCE((SELECT SUM(sc.amount) FROM project_transactions sc
			JOIN project_transactions sp ON sp.id = sc.parent_project_transaction_id
			WHERE sp.linked_transaction_id = %[1]s.id), 0)
		  ELSE 0 END, 0))`, txAlias, EventBillPredicate(txAlias))
}
