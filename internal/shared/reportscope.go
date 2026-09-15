package shared

import "fmt"

// ReportScopePredicate is the ONE report-scope filter for shared wallets
// (spec §14/5 engineering guardrail). Every personal aggregate —
// transactions summary, budgets spent, budgets overview, dashboard,
// export — must include this fragment instead of a bare
// `user_id = <caller>` clause. Inlining the filter per-query is a
// code-review blocking rule for this module.
//
// A transactions row counts toward user U's personal aggregates iff
//
//   - U's governing membership on the row's account has
//     report_scope = 'all' (every row on the wallet counts), OR
//   - report_scope = 'own' AND the row was authored by U.
//
// Rows on accounts where U's scope is 'none' — or where U has no
// membership at all — are excluded. Personal accounts are backfilled
// with an owner row at 'all' (migration 000039), so single-member
// behavior is identical to the pre-shared-wallets `user_id = U` filter.
//
// The governing membership is the ACTIVE row when one exists, else the
// most recent left row (ex-members keep the toggle, capped at 'own' by
// the API — see accounts report-scope endpoint). Pending invites
// (joined_at IS NULL) never count.
//
// txAlias is the transactions table alias in the caller's query (e.g.
// "t"); userParam is the positional parameter holding the caller's user
// id (e.g. "$1") — it may already be used elsewhere in the query.
func ReportScopePredicate(txAlias, userParam string) string {
	// Correlated scalar subquery: pick the governing membership row,
	// translate its scope into a boolean; no row → NULL → FALSE.
	return fmt.Sprintf(`COALESCE((
		SELECT CASE
			WHEN am.report_scope = 'all' THEN TRUE
			WHEN am.report_scope = 'own' THEN %[1]s.user_id = %[2]s
			ELSE FALSE
		END
		FROM account_members am
		WHERE am.account_id = %[1]s.account_id
		  AND am.user_id = %[2]s
		  AND am.joined_at IS NOT NULL
		ORDER BY (am.left_at IS NULL) DESC, am.joined_at DESC
		LIMIT 1
	), FALSE)`, txAlias, userParam)
}

// ActiveMembershipPredicate returns a SQL boolean expression that is
// true when userParam holds an ACTIVE membership (joined, not left) on
// accountExpr. Used for read visibility and write authorization on
// shared wallets — NOT for report aggregates (use ReportScopePredicate).
//
// accountExpr is any SQL expression yielding the account id (a column
// reference like "t.account_id" or a positional param); userParam is
// the positional parameter holding the caller's user id.
func ActiveMembershipPredicate(accountExpr, userParam string) string {
	return fmt.Sprintf(`EXISTS (
		SELECT 1 FROM account_members am
		WHERE am.account_id = %s
		  AND am.user_id = %s
		  AND am.joined_at IS NOT NULL
		  AND am.left_at IS NULL
	)`, accountExpr, userParam)
}
