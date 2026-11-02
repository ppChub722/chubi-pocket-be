package shared

import "fmt"

// PaperBookPredicate — the transaction is a book of a paper (a split or event bill).
func PaperBookPredicate(txAlias string) string {
	return fmt.Sprintf(`(%s.split_transaction_id IS NOT NULL)`, txAlias)
}

// ShareAmountExpr — what a row counts in reports / budgets / list totals: my share of a paper's
// bill (my_share), else the whole amount.
func ShareAmountExpr(txAlias string) string {
	return fmt.Sprintf(`COALESCE(%[1]s.my_share, %[1]s.amount)`, txAlias)
}
