package shared

import "fmt"

// EventBillPredicate — the transaction aliased txAlias is an event bill at
// full amount: it's linked to a board row whose actor is the
// transaction's author and whose amount it matches (a pulled bill). The
// board's member splits come off its share; splits added on it later are a
// separate layer on that share (owner 2026-10-10).
func EventBillPredicate(txAlias string) string {
	return fmt.Sprintf(`EXISTS (SELECT 1 FROM project_transactions ep
		JOIN project_members epm ON epm.id = ep.transaction_member_id
		WHERE ep.id = %[1]s.source_project_transaction_id
		  AND ep.parent_project_transaction_id IS NULL
		  AND epm.user_id = %[1]s.user_id AND ep.amount = %[1]s.amount)`, txAlias)
}

// ShareAmountExpr — "my share" of the transaction aliased txAlias (spec 12
// §4.5: accounts stay cash-basis, reports compute share-basis). The cash
// amount minus what others owe on it:
//   - the bill's own split debts in its author's book (forgiven ones don't
//     count — the author absorbed them);
//   - for an event bill (EventBillPredicate), the other members' board
//     splits too (both layers come off). A copy already at its share basis
//     only loses its own splits.
//
// Floored at 0. Every spending aggregate (summary, list totals, budgets,
// a wallet's summary) and the `my_share` field go through this fragment.
func ShareAmountExpr(txAlias string) string {
	return fmt.Sprintf(`GREATEST(%[1]s.amount
		- COALESCE((SELECT SUM(sd.amount) FROM personal_debts sd
			WHERE sd.source_transaction_id = %[1]s.id AND sd.user_id = %[1]s.user_id
			  AND sd.status <> 'cancelled'), 0)
		- CASE WHEN %[2]s THEN COALESCE((SELECT SUM(sc.amount) FROM project_transactions sc
			WHERE sc.parent_project_transaction_id = %[1]s.source_project_transaction_id), 0)
		  ELSE 0 END, 0)`, txAlias, EventBillPredicate(txAlias))
}
