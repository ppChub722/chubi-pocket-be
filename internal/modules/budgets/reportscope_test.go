package budgets

// Shared-wallets report-scope tests for the budgets spent computation
// (spec §14/5 — the ONE predicate must also govern budgets). Runs
// against the local dev Postgres and SKIPs when unreachable; all fixture
// rows are fresh and torn down in t.Cleanup.

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
)

const testDefaultDSN = "postgres://chubadmin:admin1234@localhost:5432/chubi_pocket_db?sslmode=disable"

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		dsn = testDefaultDSN
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Skipf("cannot build pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("database unreachable (start docker compose db): %v", err)
	}
	var migrated bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (
		SELECT 1 FROM information_schema.tables WHERE table_name = 'account_members'
	)`).Scan(&migrated); err != nil || !migrated {
		pool.Close()
		t.Skip("account_members table missing — apply migration 000039 first")
	}
	t.Cleanup(pool.Close)
	return pool
}

func mustV7(t *testing.T) uuid.UUID {
	t.Helper()
	id, err := uuid.NewV7()
	if err != nil {
		t.Fatalf("uuid: %v", err)
	}
	return id
}

// TestComputeSpentReportScope: 1 shared wallet, 2 users, rows by both;
// verify the budget spent number under the owner's none / own / all.
func TestComputeSpentReportScope(t *testing.T) {
	pool := testPool(t)
	store := NewStore(pool)
	ctx := context.Background()

	var userIDs []uuid.UUID
	newUser := func(name string) uuid.UUID {
		id := mustV7(t)
		uniq := fmt.Sprintf("bgt_%s", id)
		if _, err := pool.Exec(ctx, `
			INSERT INTO users (id, username, email, display_name, password_hash, currency)
			VALUES ($1, $2, $3, $4, 'x', 'THB')`,
			id, uniq, uniq+"@test.local", name); err != nil {
			t.Fatalf("insert user: %v", err)
		}
		userIDs = append(userIDs, id)
		return id
	}
	t.Cleanup(func() {
		for _, q := range []string{
			`DELETE FROM transactions WHERE user_id = ANY($1)`,
			`DELETE FROM account_members WHERE user_id = ANY($1)`,
			`DELETE FROM accounts WHERE user_id = ANY($1)`,
			`DELETE FROM categories WHERE user_id = ANY($1)`,
			`DELETE FROM users WHERE id = ANY($1)`,
		} {
			if _, err := pool.Exec(ctx, q, userIDs); err != nil {
				t.Errorf("cleanup %q: %v", q, err)
			}
		}
	})

	newAccount := func(owner uuid.UUID, name string) uuid.UUID {
		id := mustV7(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO accounts (id, user_id, name, type, balance, currency, status)
			VALUES ($1, $2, $3, 'bank', 0, 'THB', 'active')`, id, owner, name); err != nil {
			t.Fatalf("insert account: %v", err)
		}
		return id
	}
	addMember := func(account, user uuid.UUID, role, scope string) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO account_members (id, account_id, user_id, role, report_scope, joined_at)
			VALUES ($1, $2, $3, $4, $5, NOW())`,
			mustV7(t), account, user, role, scope); err != nil {
			t.Fatalf("insert member: %v", err)
		}
	}
	newCategory := func(user uuid.UUID, name string) uuid.UUID {
		id := mustV7(t)
		if _, err := pool.Exec(ctx, `
			INSERT INTO categories (id, user_id, name, type, is_system)
			VALUES ($1, $2, $3, 'expense', FALSE)`, id, user, name); err != nil {
			t.Fatalf("insert category: %v", err)
		}
		return id
	}
	insertExpense := func(user, account, category uuid.UUID, amount float64) {
		if _, err := pool.Exec(ctx, `
			INSERT INTO transactions (id, user_id, account_id, type, amount, category_id, date)
			VALUES ($1, $2, $3, 'expense', $4, $5, '2026-01-15'::date)`,
			mustV7(t), user, account, amount, category); err != nil {
			t.Fatalf("insert tx: %v", err)
		}
	}

	owner := newUser("BudgetOwner")
	member := newUser("BudgetMember")

	wallet := newAccount(owner, "Shared wallet")
	addMember(wallet, owner, "owner", "all")
	addMember(wallet, member, "member", "none")
	personal := newAccount(owner, "Personal")
	addMember(personal, owner, "owner", "all")

	ownerCat := newCategory(owner, "Budget Food")
	memberCat := newCategory(member, "Member Food")

	insertExpense(owner, wallet, ownerCat, 100)   // owner's wallet spending
	insertExpense(owner, personal, ownerCat, 10)  // owner's personal spending
	insertExpense(member, wallet, memberCat, 40)  // other member's row — never matches owner's category

	budget := &Budget{
		UserID:     owner,
		CategoryID: ownerCat,
		Scope:      ScopeUser,
		Amount:     500,
		Period:     PeriodMonthly,
	}
	setScope := func(scope string) {
		if _, err := pool.Exec(ctx, `
			UPDATE account_members SET report_scope = $1
			WHERE account_id = $2 AND user_id = $3`, scope, wallet, owner); err != nil {
			t.Fatalf("set scope: %v", err)
		}
	}
	spent := func() float64 {
		res, err := store.ComputeSpent(ctx, owner, budget, "2026-01-01", "2026-01-31")
		if err != nil {
			t.Fatalf("compute spent: %v", err)
		}
		return res.Total
	}

	cases := []struct {
		scope string
		want  float64
	}{
		{"none", 10}, // wallet hidden from the owner's budgets
		{"own", 110}, // own wallet rows roll into the category budget
		{"all", 110}, // other members' rows carry THEIR categories — totals only, never budgets
	}
	for _, c := range cases {
		setScope(c.scope)
		if got := spent(); got != c.want {
			t.Errorf("scope=%s: spent = %.2f, want %.2f", c.scope, got, c.want)
		}
	}
}
