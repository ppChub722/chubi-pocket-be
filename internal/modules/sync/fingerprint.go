// Package sync tells the app which shared datasets changed (fingerprints), serves them in one call
// (POST /sync) and stamps every write response with the caller's fingerprints (X-Sync-Versions).
package sync

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// A datasets: shared, unfiltered data the app keeps on the device. Names are the wire names.
const (
	Accounts     = "accounts"
	Categories   = "categories"
	Tags         = "tags"
	Contacts     = "contacts"
	DebtPeople   = "debt_people"
	Projects     = "projects"
	Recurring    = "recurring"
	FormDefaults = "form_defaults"
	Providers    = "providers"
	// Profile (0.4.1-2 P4) — GET /users/me: every preference, the switches, the main wallet.
	Profile = "profile"
)

// B keys: page data. Fingerprints only; the app reloads the pages with its existing endpoints.
const (
	Transactions = "transactions"
	Budgets      = "budgets"
	SavingGoals  = "saving_goals"
	Pending      = "pending"
)

// DatasetNames is every A dataset in a stable order.
var DatasetNames = []string{Accounts, Categories, Tags, Contacts, DebtPeople, Projects, Recurring, FormDefaults, Providers, Profile}

var keyNames = append(append([]string{}, DatasetNames...), Transactions, Budgets, SavingGoals, Pending)

// Rows the caller can see. $1 is always the caller's user id.
const (
	visAcc = `(SELECT am.account_id FROM account_members am
		WHERE am.user_id = $1 AND am.joined_at IS NOT NULL AND am.left_at IS NULL)`
	visProj    = `(SELECT pm.scope_id FROM project_members pm WHERE pm.user_id = $1)`
	visTx      = `(SELECT vt.id FROM transactions vt WHERE vt.user_id = $1 OR vt.account_id IN ` + visAcc + `)`
	linkedUser = `(SELECT lc.linked_user_id FROM contacts lc WHERE lc.user_id = $1 AND lc.linked_user_id IS NOT NULL)`
	// prefKeys — only the named preference keys of the caller (P4: a page_prefs or theme write
	// must not re-sync the sets that read one key).
	prefKeys = `(SELECT up.user_id, %s FROM user_preferences up) x WHERE x.user_id = $1`
)

func prefs(keys ...string) source {
	cols := make([]string, len(keys))
	for i, k := range keys {
		cols[i] = fmt.Sprintf(`up.preferences->'%s' AS %s`, k, k)
	}
	return content(fmt.Sprintf(prefKeys, strings.Join(cols, ", ")))
}

type kind int

const (
	// byTime: max(updated_at) in microseconds + row count. Hard deletes show as a lower count.
	byTime kind = iota
	// byContent: sum of a hash of every row + row count, for tables without updated_at.
	byContent
)

type source struct {
	kind kind
	from string // FROM clause body; the table alias is x
}

func ts(from string) source      { return source{byTime, from} }
func content(from string) source { return source{byContent, from} }

// sources lists, per dataset or B key, every table its data is read from (rows visible to the caller),
// including tables that only supply names or icons. A table missing here means the data can change
// without the fingerprint changing.
var sources = map[string][]source{
	Accounts: {
		ts(`accounts x WHERE x.id IN ` + visAcc),
		ts(`account_members x WHERE x.account_id IN ` + visAcc),
		ts(`users x WHERE x.id = $1 OR x.id IN (SELECT mm.user_id FROM account_members mm
			WHERE mm.account_id IN ` + visAcc + ` AND mm.joined_at IS NOT NULL AND mm.left_at IS NULL)`),
		ts(`transactions x WHERE x.account_id IN ` + visAcc),
		ts(`saving_entries x WHERE x.user_id = $1`),
		ts(`saving_goals x WHERE x.user_id = $1`),
	},
	Categories: {
		ts(`categories x WHERE x.user_id = $1`),
		ts(`transactions x WHERE x.user_id = $1`),
		ts(`account_members x WHERE x.user_id = $1`),
		ts(`accounts x WHERE x.id IN ` + visAcc),
		ts(`personal_debts x WHERE x.user_id = $1`),
		ts(`project_transactions x WHERE x.scope_id IN ` + visProj),
	},
	Tags: {
		ts(`tags x WHERE x.user_id = $1`),
		ts(`transaction_tags x WHERE x.tag_id IN (SELECT tg.id FROM tags tg WHERE tg.user_id = $1)`),
	},
	Contacts: {
		ts(`contacts x WHERE x.user_id = $1`),
		ts(`users x WHERE x.id IN ` + linkedUser),
	},
	DebtPeople: {
		ts(`personal_debts x WHERE x.user_id = $1`),
		ts(`contacts x WHERE x.user_id = $1`),
		ts(`users x WHERE x.id IN ` + linkedUser),
	},
	Projects: {
		ts(`projects x WHERE x.id IN ` + visProj),
		ts(`project_members x WHERE x.scope_id IN ` + visProj),
		ts(`project_transactions x WHERE x.scope_id IN ` + visProj),
		ts(`transactions x WHERE x.user_id = $1 AND (x.project_id IN ` + visProj + ` OR x.split_transaction_id IN ` + visProj + `)`),
		content(`project_my_tags x WHERE x.user_id = $1`),
		ts(`tags x WHERE x.user_id = $1`),
	},
	Recurring: {
		ts(`recurring_rules x WHERE x.user_id = $1`),
		ts(`recurring_items x WHERE x.user_id = $1`),
		ts(`accounts x WHERE x.id IN ` + visAcc),
		ts(`categories x WHERE x.user_id = $1`),
		ts(`tags x WHERE x.user_id = $1`),
		ts(`saving_goals x WHERE x.user_id = $1`),
		prefs("timezone", "language"),               // occurrence names and today
		ts(`contacts x WHERE x.user_id = $1`),       // a "หนี้" setting's person
		ts(`personal_debts x WHERE x.user_id = $1`), // its balance and filled slots
	},
	FormDefaults: {
		ts(`transactions x WHERE x.user_id = $1`),
		ts(`accounts x WHERE x.id IN ` + visAcc),
		ts(`account_members x WHERE x.user_id = $1`),
		ts(`categories x WHERE x.user_id = $1`),
		prefs("fee_category_id"),
		content(`(SELECT ns.user_id, ns.default_account_id FROM user_notification_settings ns) x WHERE x.user_id = $1`), // X10 main wallet
	},
	Providers: {
		ts(`payment_providers x`),
	},
	Profile: {
		ts(`users x WHERE x.id = $1`),
		ts(`user_preferences x WHERE x.user_id = $1`),
		ts(`user_notification_settings x WHERE x.user_id = $1`),
		ts(`accounts x WHERE x.id IN ` + visAcc), // the main wallet's name / icon
		ts(`categories x WHERE x.user_id = $1`),  // the fee category's name / icon
	},
	Transactions: {
		ts(`transactions x WHERE x.id IN ` + visTx),
		ts(`transaction_tags x WHERE x.transaction_id IN ` + visTx),
		ts(`personal_debts x WHERE x.user_id = $1`),
		ts(`categories x WHERE x.user_id = $1`),
		ts(`tags x WHERE x.user_id = $1`),
		ts(`accounts x WHERE x.id IN ` + visAcc),
		ts(`projects x WHERE x.id IN ` + visProj),
	},
	Budgets: {
		ts(`budgets x WHERE x.user_id = $1`),
		content(`budget_periods x WHERE x.budget_id IN (SELECT b.id FROM budgets b WHERE b.user_id = $1)`),
		content(`budget_categories x WHERE x.budget_id IN (SELECT b.id FROM budgets b WHERE b.user_id = $1)`),
		content(`budget_wallets x WHERE x.budget_id IN (SELECT b.id FROM budgets b WHERE b.user_id = $1)`),
	},
	SavingGoals: {
		ts(`saving_goals x WHERE x.user_id = $1`),
		ts(`saving_entries x WHERE x.user_id = $1`),
		// a goal's month plan comes from its recurring saving settings
		ts(`recurring_rules x WHERE x.user_id = $1 AND x.type = 'saving'`),
	},
	Pending: {
		ts(`pending_transactions x WHERE x.user_id = $1`),
	},
}

// byDay are the sets whose data also depends on today in the user's timezone (this month's figures,
// days until a due date, days outstanding), so a new day must re-sync them.
var byDay = map[string]bool{Accounts: true, Categories: true, Recurring: true}

var fingerprintSQL, fingerprintIndex = buildFingerprintSQL()

type slot struct{ key string }

func buildFingerprintSQL() (string, []slot) {
	var (
		parts []string
		index []slot
	)
	for _, key := range keyNames {
		for _, s := range sources[key] {
			expr := `COALESCE(MAX((EXTRACT(EPOCH FROM x.updated_at) * 1000000)::bigint), 0)`
			if s.kind == byContent {
				expr = `COALESCE(SUM(hashtext(x::text)), 0)::bigint`
			}
			parts = append(parts, fmt.Sprintf(`SELECT %d AS i, %s AS v, COUNT(*) AS n FROM %s`, len(index), expr, s.from))
			index = append(index, slot{key})
		}
	}
	return strings.Join(parts, "\nUNION ALL\n") + "\nORDER BY i", index
}

// Fingerprints computes the fingerprint of every A dataset and B key for one user in one query.
// A fingerprint is compared, never parsed.
func Fingerprints(ctx context.Context, db *pgxpool.Pool, userID uuid.UUID, tz string) (map[string]string, error) {
	rows, err := db.Query(ctx, fingerprintSQL, userID)
	if err != nil {
		return nil, fmt.Errorf("sync fingerprints: %w", err)
	}
	parts := make(map[string][]string, len(keyNames))
	for rows.Next() {
		var (
			i    int
			v, n int64
		)
		if err := rows.Scan(&i, &v, &n); err != nil {
			rows.Close()
			return nil, fmt.Errorf("sync fingerprints scan: %w", err)
		}
		key := fingerprintIndex[i].key
		parts[key] = append(parts[key], fmt.Sprintf("%d-%d", v, n))
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("sync fingerprints: %w", err)
	}
	today, err := shared.UserToday(ctx, db, userID, tz)
	if err != nil {
		return nil, err
	}
	var debtDay string
	if err := db.QueryRow(ctx, `SELECT `+personal_debts.TodayInUserTZSQL+`::text`, userID).Scan(&debtDay); err != nil {
		return nil, fmt.Errorf("sync debt day: %w", err)
	}
	out := make(map[string]string, len(keyNames))
	for _, key := range keyNames {
		p := parts[key]
		switch {
		case byDay[key]:
			p = append(p, today.Format("20060102"))
		case key == DebtPeople:
			p = append(p, strings.ReplaceAll(debtDay, "-", ""))
		}
		out[key] = strings.Join(p, ".")
	}
	return out, nil
}
