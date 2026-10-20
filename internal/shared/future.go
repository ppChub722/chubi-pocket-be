package shared

// A transaction may be dated in the future: it is stored (and moves the cached
// accounts.balance at once) but must not count anywhere until its day. These
// helpers build the SQL for that: "today" in the caller's time zone, and a
// wallet's balance as of today.

// TodaySQL is the caller's today as a date; userParam is the SQL parameter
// that holds the user id (e.g. "$1").
func TodaySQL(userParam string) string {
	return `(NOW() AT TIME ZONE COALESCE(
	(SELECT z.name FROM pg_timezone_names z JOIN user_preferences up ON up.preferences->>'timezone' = z.name
	 WHERE up.user_id = ` + userParam + ` LIMIT 1), 'Asia/Bangkok'))::date`
}

// SignedTxSQL is a transaction's effect on its wallet: + income, - expense, and
// for a transfer the sign of its side (TRANSFER_OUT is minus). txAlias is the
// transactions alias.
func SignedTxSQL(txAlias string) string {
	return `(CASE ` + txAlias + `.type WHEN 'income' THEN ` + txAlias + `.amount WHEN 'expense' THEN -` + txAlias + `.amount
		WHEN 'transfer' THEN CASE WHEN (SELECT fc.system_kind FROM categories fc WHERE fc.id = ` + txAlias + `.category_id) = 'TRANSFER_OUT'
			THEN -` + txAlias + `.amount ELSE ` + txAlias + `.amount END
		ELSE 0 END)`
}

// FutureDeltaSQL is Σ of the signed future transactions of the wallet
// accountExpr (e.g. "a.id") as of the caller's today — what the cached balance
// already holds but is not real yet.
func FutureDeltaSQL(accountExpr, userParam string) string {
	return `COALESCE((SELECT SUM(` + SignedTxSQL("ft") + `) FROM transactions ft
		WHERE ft.account_id = ` + accountExpr + ` AND ft.date > ` + TodaySQL(userParam) + `), 0)`
}

// EffectiveBalanceSQL is the wallet's balance as of today: the cached
// balance minus its future transactions. balanceExpr is e.g. "a.balance".
func EffectiveBalanceSQL(balanceExpr, accountExpr, userParam string) string {
	return `(` + balanceExpr + ` - ` + FutureDeltaSQL(accountExpr, userParam) + `)`
}
