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
		(SELECT GREATEST(LEAST(%[1]s.amount, COALESCE((SELECT SUM(bp.share) FROM split_transaction_people bp
				WHERE bp.split_transaction_id = bb.id AND bp.user_id = %[1]s.user_id), 0))
			- COALESCE((SELECT SUM(sd.amount) FROM personal_debts sd
				WHERE sd.source_transaction_id = %[1]s.id AND sd.user_id = %[1]s.user_id), 0), 0)
		 FROM project_transactions bb
		 WHERE bb.id = %[1]s.split_transaction_id AND %[1]s.source_project_transaction_id IS NOT NULL
		   AND bb.type <> 'payback'
		   AND EXISTS (SELECT 1 FROM split_transaction_people bm
				WHERE bm.split_transaction_id = bb.id AND bm.user_id = %[1]s.user_id)),
		GREATEST(%[1]s.amount
		- COALESCE((SELECT SUM(sd.amount) FROM personal_debts sd
			WHERE sd.source_transaction_id = %[1]s.id AND sd.user_id = %[1]s.user_id), 0)
		- CASE WHEN %[2]s THEN COALESCE((SELECT SUM(sc.share) FROM split_transaction_people sc
			JOIN project_transactions sp ON sp.id = sc.split_transaction_id
			WHERE sp.linked_transaction_id = %[1]s.id
			  AND sc.event_member_id IS DISTINCT FROM sp.transaction_member_id), 0)
		  ELSE 0 END, 0))`, txAlias, EventBillPredicate(txAlias))
}
