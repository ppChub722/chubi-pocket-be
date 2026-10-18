package shared

import "fmt"

func ReportScopePredicate(txAlias, userParam string) string {
	return fmt.Sprintf(`CASE WHEN %[1]s.account_id IS NULL THEN %[1]s.user_id = %[2]s
	WHEN EXISTS (SELECT 1 FROM accounts ra WHERE ra.id = %[1]s.account_id AND ra.status = 'archived') THEN FALSE
	ELSE COALESCE((
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
	), FALSE) END`, txAlias, userParam)
}

func ReportableCategoryPredicate(catAlias string) string {
	return fmt.Sprintf("COALESCE(%s.include_in_report, TRUE)", catAlias)
}

func ActiveMembershipPredicate(accountExpr, userParam string) string {
	return fmt.Sprintf(`EXISTS (
		SELECT 1 FROM account_members am
		WHERE am.account_id = %s
		  AND am.user_id = %s
		  AND am.joined_at IS NOT NULL
		  AND am.left_at IS NULL
	)`, accountExpr, userParam)
}
