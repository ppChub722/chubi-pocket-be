package me

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
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
	exec(`INSERT INTO budgets (id, user_id, name, amount, recurring, freq, start_date, anchor_date, status) VALUES
		(gen_random_uuid(), $1, 'b1', 100, TRUE, 'monthly', CURRENT_DATE, CURRENT_DATE, 'active'),
		(gen_random_uuid(), $1, 'b2', 100, TRUE, 'weekly', CURRENT_DATE, CURRENT_DATE, 'archived')`, uid)
	exec(`INSERT INTO budget_categories (budget_id, category_id) SELECT id, $2 FROM budgets WHERE user_id = $1`, uid, cat)

	// Debts: two people with something open; Dee is square and labelled paid.
	exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, currency, date, paid)
		VALUES (gen_random_uuid(), $1, 'paid', 'Bob', 100, 'THB', CURRENT_DATE, FALSE),
		       (gen_random_uuid(), $1, 'paid', 'Cee', 100, 'THB', CURRENT_DATE, TRUE),
		       (gen_random_uuid(), $1, 'paid', 'Dee', 100, 'THB', CURRENT_DATE, TRUE),
		       (gen_random_uuid(), $1, 'received', 'Dee', 100, 'THB', CURRENT_DATE, TRUE)`, uid)

	// Savings: wallet balance 1000; 500 saved into goal A, 200 into B, 100
	// spare → saved 800, in goals 700, available 200.
	acc := testdb.Account(t, pool, uid)
	exec(`UPDATE accounts SET balance = 1000 WHERE id = $1`, acc)
	goalA, goalB := uuid.New(), uuid.New()
	exec(`INSERT INTO saving_goals (id, user_id, name, target_amount, status) VALUES
		($2, $1, 'A', 1000, 'active'), ($3, $1, 'B', 200, 'active'),
		(gen_random_uuid(), $1, 'Old', 500, 'archived')`, uid, goalA, goalB)
	exec(`INSERT INTO saving_entries (id, user_id, group_id, kind, account_id, from_bucket, to_bucket, to_goal_id, amount, date) VALUES
		(gen_random_uuid(), $1, gen_random_uuid(), 'save', $2, 'available', 'goal', $3, 500, CURRENT_DATE),
		(gen_random_uuid(), $1, gen_random_uuid(), 'save', $2, 'available', 'goal', $4, 200, CURRENT_DATE),
		(gen_random_uuid(), $1, gen_random_uuid(), 'save', $2, 'available', 'spare', NULL, 100, CURRENT_DATE)`, uid, acc, goalA, goalB)

	// Badges.
	exec(`INSERT INTO notifications (id, recipient_user_id, type, payload) VALUES (gen_random_uuid(), $1, 'project_added', '{}')`, uid)
	exec(`INSERT INTO pending_transactions (id, user_id, source) VALUES (gen_random_uuid(), $1, 'text')`, uid)

	got := get()
	want := Counts{Categories: 1, Tags: 2, Contacts: 1, ActiveEvents: 1, OpenDebts: 2, Budgets: 1,
		Goals: shared.SavingTotals{Count: 2, Saved: 800, InGoals: 700, Spare: 100, Available: 200}}
	if got.Counts != want {
		t.Errorf("counts = %+v, want %+v", got.Counts, want)
	}
	if got.Badges.UnreadNotifications != 1 || got.Badges.PendingDrafts != 1 {
		t.Errorf("badges = %+v", got.Badges)
	}
}

// The More tiles' figures: debts net, budgets tile, recurring counts — the
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
		_, _ = pool.Exec(ctx, `DELETE FROM recurring_rules WHERE user_id = $1`, uid)
		_, _ = pool.Exec(ctx, `DELETE FROM recurring_items WHERE user_id = $1`, uid)
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

	// Debts: I paid Aom 100, Aom paid back 30, Bee paid 40 for me → owed 70, owe 40, net 30.
	exec(`INSERT INTO personal_debts (id, user_id, direction, counterparty_person_name, amount, currency, date)
		VALUES (gen_random_uuid(), $1, 'paid', 'Aom', 100, 'THB', CURRENT_DATE),
		       (gen_random_uuid(), $1, 'received', 'Aom', 30, 'THB', CURRENT_DATE),
		       (gen_random_uuid(), $1, 'received', 'Bee', 40, 'THB', CURRENT_DATE)`, uid)
	// A budget of 100 on Food with 150 spent this month → over the limit.
	acct := testdb.Account(t, pool, uid)
	food := testdb.Category(t, pool, uid, "Food")
	exec(`WITH n AS (INSERT INTO budgets (id, user_id, name, amount, recurring, freq, start_date, anchor_date)
			VALUES (gen_random_uuid(), $1, 'Food', 100, TRUE, 'monthly', date_trunc('month', CURRENT_DATE)::date,
			        date_trunc('month', CURRENT_DATE)::date) RETURNING id)
		INSERT INTO budget_categories (budget_id, category_id) SELECT id, $2 FROM n`, uid, food)
	exec(`INSERT INTO transactions (id, user_id, account_id, type, amount, category_id, date)
		VALUES (gen_random_uuid(), $1, $2, 'expense', 150, $3, CURRENT_DATE)`, uid, acct, food)
	// Recurring: an item in 3 days, one in 60 days, one of a paused setting;
	// plus a one-off upcoming item in 5 days.
	for _, c := range []struct {
		name   string
		days   int
		status string
	}{{"soon", 3, "active"}, {"later", 60, "active"}, {"paused", 3, "paused"}} {
		exec(`WITH r AS (INSERT INTO recurring_rules (id, user_id, name, type, amount, account_id, freq, start_date, status)
				VALUES (gen_random_uuid(), $1, $2, 'expense', 10, $3, 'monthly', CURRENT_DATE + $4::int, $5) RETURNING id)
			INSERT INTO recurring_items (id, user_id, rule_id, seq, slot, date, draft)
			SELECT gen_random_uuid(), $1, id, 1, CURRENT_DATE + $4::int, CURRENT_DATE + $4::int, '{}' FROM r`,
			uid, c.name, acct, c.days, c.status)
	}
	exec(`INSERT INTO recurring_items (id, user_id, date, draft) VALUES (gen_random_uuid(), $1, CURRENT_DATE + 5, '{}')`, uid)

	got := get().Counts
	if got.DebtsSummary.OwedToMe != 70 || got.DebtsSummary.IOwe != 40 || got.DebtsSummary.Net != 30 {
		t.Errorf("debts_summary = %+v, want owed 70 / owe 40 / net 30", got.DebtsSummary)
	}
	if got.BudgetsSummary.TotalBudget != 100 || got.BudgetsSummary.TotalSpent != 150 || got.BudgetsSummary.OverLimitCount != 1 {
		t.Errorf("budgets_summary = %+v, want budget 100 / spent 150 / 1 over", got.BudgetsSummary)
	}
	if got.Recurring.ActiveCount != 2 || got.Recurring.UpcomingCount != 2 {
		t.Errorf("recurring = %+v, want 2 active / 2 upcoming (the paused one never counts)", got.Recurring)
	}
}
