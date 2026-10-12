package me

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestMyCounts(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	h := NewHandler(NewService(pool))
	r := apitest.Engine(uid, func(g *gin.RouterGroup) { g.GET("/me/counts", h.Counts) })

	get := func() MeCounts {
		t.Helper()
		status, raw := apitest.Do(t, r, "GET", "/api/v1/me/counts", nil)
		if status != http.StatusOK {
			t.Fatalf("status %d: %s", status, raw)
		}
		apitest.Conform(t, "getMyCounts", status, raw)
		var out MeCounts
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}

	// Empty user: everything zero.
	if got := get(); got.Counts != (Counts{}) || got.Badges.UnreadNotifications != 0 {
		t.Fatalf("empty = %+v", got)
	}

	// Categories: active non-system counted; archived and system not.
	cat := testdb.Category(t, pool, uid, "Food")
	testdb.Category(t, pool, uid, "Gone")
	exec(`UPDATE categories SET status = 'archived' WHERE user_id = $1 AND name = 'Gone'`, uid)
	exec(`INSERT INTO categories (id, user_id, name, type, is_system, system_kind) VALUES (gen_random_uuid(), $1, 'Sys', 'expense', TRUE, 'debt_repayment')`, uid)

	// Tags, contacts (one archived).
	exec(`INSERT INTO tags (id, user_id, name) VALUES (gen_random_uuid(), $1, 'a'), (gen_random_uuid(), $1, 'b')`, uid)
	exec(`INSERT INTO contacts (id, user_id, display_name, status) VALUES (gen_random_uuid(), $1, 'C1', 'active'), (gen_random_uuid(), $1, 'C2', 'archived')`, uid)

	// Events: one active (owner member), one completed.
	exec(`WITH p AS (INSERT INTO projects (id, owner_user_id, name, status) VALUES (gen_random_uuid(), $1, 'Live', 'active') RETURNING id)
		INSERT INTO project_members (id, project_id, user_id, display_name, role, status) SELECT gen_random_uuid(), id, $1, 'Me', 'owner', 'active' FROM p`, uid)
	exec(`INSERT INTO projects (id, owner_user_id, name, status) VALUES (gen_random_uuid(), $1, 'Done', 'completed')`, uid)

	// Budgets: one active, one archived.
	exec(`INSERT INTO budgets (id, user_id, name, category_id, scope, amount, period, currency, status) VALUES
		(gen_random_uuid(), $1, 'b1', $2, 'user', 100, 'monthly', 'THB', 'active'),
		(gen_random_uuid(), $1, 'b2', $2, 'user', 100, 'weekly', 'THB', 'archived')`, uid, cat)

	// Debts: two open, one closed.
	for _, st := range []string{"open", "open", "settled"} {
		exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, settled_amount, currency, status)
			VALUES (gen_random_uuid(), $1, 'owed_to_me', 'Bob', 100, 0, 'THB', $2)`, uid, st)
	}

	// Goals: wallet balance 1000; goal A 50% of it vs target 1000 -> current 500;
	// goal B 100% vs target 200 -> current capped at 200. progress = 700/1200.
	acc := testdb.Account(t, pool, uid)
	exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, acc)
	exec(`INSERT INTO saving_goals (id, user_id, name, target_amount, linked_account_id, allocation_pct, status) VALUES
		(gen_random_uuid(), $1, 'A', 1000, $2, 50, 'active'),
		(gen_random_uuid(), $1, 'B', 200, $2, 100, 'active'),
		(gen_random_uuid(), $1, 'Old', 500, $2, 10, 'archived')`, uid, acc)

	// Badges.
	exec(`INSERT INTO notifications (id, recipient_user_id, type, payload) VALUES (gen_random_uuid(), $1, 'project_added', '{}')`, uid)
	exec(`INSERT INTO pending_transactions (id, user_id, source) VALUES (gen_random_uuid(), $1, 'text')`, uid)

	got := get()
	want := Counts{Categories: 1, Tags: 2, Contacts: 1, ActiveEvents: 1, OpenDebts: 2, Budgets: 1,
		Goals: GoalsCount{Count: 2, ProgressPct: 58.33}}
	if got.Counts != want {
		t.Errorf("counts = %+v, want %+v", got.Counts, want)
	}
	if got.Badges.UnreadNotifications != 1 || got.Badges.PendingDrafts != 1 {
		t.Errorf("badges = %+v", got.Badges)
	}
}

// The More tiles' figures: debts net, budgets tile, scheduled counts — the
// same numbers the Home tiles show.
func TestMyCountsTileFigures(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	svc := NewService(pool).
		WithBudgets(budgets.NewService(budgets.NewStore(pool))).
		WithDebts(personal_debts.NewService(personal_debts.NewStore(pool), txs))
	h := NewHandler(svc)
	r := apitest.Engine(uid, func(g *gin.RouterGroup) { g.GET("/me/counts", h.Counts) })
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM scheduled_transactions WHERE user_id = $1`, uid)
		_, _ = pool.Exec(ctx, `DELETE FROM budgets WHERE user_id = $1`, uid)
		_, _ = pool.Exec(ctx, `DELETE FROM personal_debts WHERE user_id = $1`, uid)
	})
	get := func() MeCounts {
		t.Helper()
		status, raw := apitest.Do(t, r, "GET", "/api/v1/me/counts", nil)
		apitest.Conform(t, "getMyCounts", status, raw)
		var out MeCounts
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	// Debts: owed to me 100 (30 repaid) and I owe 40 → owed 70, owe 40, net 30.
	exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, settled_amount, currency, status)
		VALUES (gen_random_uuid(), $1, 'owed_to_me', 'Aom', 100, 30, 'THB', 'open'),
		       (gen_random_uuid(), $1, 'i_owe', 'Bee', 40, 0, 'THB', 'open')`, uid)
	// A budget of 100 on Food with 150 spent this month → over the limit.
	acct := testdb.Account(t, pool, uid)
	food := testdb.Category(t, pool, uid, "Food")
	exec(`INSERT INTO budgets (id, user_id, category_id, scope, amount, period, currency, name)
		VALUES (gen_random_uuid(), $1, $2, 'user', 100, 'monthly', 'THB', 'Food')`, uid, food)
	exec(`INSERT INTO transactions (id, user_id, account_id, type, amount, category_id, date)
		VALUES (gen_random_uuid(), $1, $2, 'expense', 150, $3, CURRENT_DATE)`, uid, acct, food)
	// Scheduled: one due in 3 days, one in 60 days, one paused.
	for _, c := range []struct {
		name   string
		days   int
		status string
	}{{"soon", 3, "active"}, {"later", 60, "active"}, {"paused", 3, "paused"}} {
		exec(`INSERT INTO scheduled_transactions (id, user_id, account_id, name, type, entry_type, amount, billing_cycle, next_billing_date, status)
			VALUES (gen_random_uuid(), $1, $2, $3, 'expense', 'recurring', 10, 'monthly', CURRENT_DATE + $4::int, $5)`,
			uid, acct, c.name, c.days, c.status)
	}

	got := get().Counts
	if got.DebtsSummary.OwedToMe != 70 || got.DebtsSummary.IOwe != 40 || got.DebtsSummary.Net != 30 {
		t.Errorf("debts_summary = %+v, want owed 70 / owe 40 / net 30", got.DebtsSummary)
	}
	if got.BudgetsSummary.TotalBudget != 100 || got.BudgetsSummary.TotalSpent != 150 || got.BudgetsSummary.OverLimitCount != 1 {
		t.Errorf("budgets_summary = %+v, want budget 100 / spent 150 / 1 over", got.BudgetsSummary)
	}
	if got.Scheduled.ActiveCount != 2 || got.Scheduled.UpcomingCount != 1 {
		t.Errorf("scheduled = %+v, want 2 active / 1 upcoming (the paused one never counts)", got.Scheduled)
	}
}
