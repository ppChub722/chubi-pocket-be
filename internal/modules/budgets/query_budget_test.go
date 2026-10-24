package budgets

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// countingExpected is an Expecter that counts how often it is asked.
type countingExpected struct {
	inner fakeExpected
	calls *int
}

func (c countingExpected) ExpectedBetween(ctx context.Context, u uuid.UUID, from, to time.Time) ([]shared.ExpectedOccurrence, error) {
	*c.calls++
	return c.inner.ExpectedBetween(ctx, u, from, to)
}

// Budgets cost the same number of queries however many there are: one pass reads their
// periods, scope categories and counted rows, and asks the Expecter once (the budget
// excludes the auth middleware's one user lookup).
func TestBudgetsQueryBudget(t *testing.T) {
	pool := testdb.StatsPool(t)
	f := &budFix{t: t, ctx: context.Background(), pool: pool}
	f.me = testdb.User(t, pool)
	f.wallet = testdb.Account(t, pool, f.me)
	f.food = testdb.Category(t, pool, f.me, "Food")
	f.cafe = testdb.Category(t, pool, f.me, "Cafe")
	f.exec(`UPDATE categories SET parent_id = $2 WHERE id = $1`, f.cafe, f.food)
	f.travel = testdb.Category(t, pool, f.me, "Travel")
	loc, _ := time.LoadLocation("Asia/Bangkok")
	now := time.Now().In(loc)
	f.today = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	t.Cleanup(func() {
		_, _ = pool.Exec(f.ctx, `DELETE FROM budgets WHERE user_id = $1`, f.me)
		_, _ = pool.Exec(f.ctx, `DELETE FROM recurring_items WHERE user_id = $1`, f.me)
	})

	calls := 0
	rule := uuid.New()
	exp := countingExpected{inner: fakeExpected{occs: []shared.ExpectedOccurrence{
		f.occ(rule, 3, 120, &f.food, &f.wallet),
		f.occ(rule, 5, 80, &f.travel, &f.wallet),
	}}, calls: &calls}
	h := NewHandler(NewService(NewStore(pool)).WithExpected(exp))
	f.r = apitest.CountedEngine(f.me, func(g *gin.RouterGroup) {
		g.GET("/budgets", h.List)
		g.POST("/budgets", h.Create)
		g.GET("/budgets/:id", h.Get)
		g.GET("/budgets/:id/items", h.Items)
	})

	monthStart := time.Date(f.today.Year(), f.today.Month(), 1, 0, 0, 0, 0, time.UTC)
	start := monthStart.AddDate(0, -2, 0).Format(dateLayout)
	f.tx(40, &f.food, f.day(0))
	f.tx(25, &f.cafe, f.day(0), "lunch")
	f.tx(300, &f.travel, f.day(0))
	bodies := []map[string]any{
		{"name": "All", "amount": 5000, "recurring": true, "rule": map[string]any{"freq": "monthly"}, "start_date": start,
			"scope": map[string]any{"all": true}},
		{"name": "Food", "amount": 1000, "recurring": true, "rule": map[string]any{"freq": "monthly"}, "start_date": start,
			"scope": map[string]any{"categories": []map[string]any{{"id": f.food, "include_children": true}}}},
		{"name": "Travel", "amount": 900, "recurring": true, "rule": map[string]any{"freq": "monthly"}, "start_date": start,
			"scope": map[string]any{"categories": []map[string]any{{"id": f.travel}}}},
		{"name": "Wallet", "amount": 700, "recurring": true, "rule": map[string]any{"freq": "monthly"}, "start_date": start,
			"scope": map[string]any{"wallet_ids": []string{f.wallet.String()}}},
		{"name": "Lunch", "amount": 300, "recurring": true, "rule": map[string]any{"freq": "monthly"}, "start_date": start,
			"scope": map[string]any{"tags": []string{"lunch"}}},
	}
	for _, b := range bodies {
		if status, raw := apitest.Do(t, f.r, "POST", "/api/v1/budgets", b); status != http.StatusCreated {
			t.Fatalf("create %v: %d %s", b["name"], status, raw)
		}
	}
	var l List
	f.call("GET", "/budgets?status=active&sort=name", "", nil, http.StatusOK, &l)
	if len(l.Data) != len(bodies) {
		t.Fatalf("%d budgets listed, want %d", len(l.Data), len(bodies))
	}
	first := l.Data[0].ID.String()

	calls = 0
	status, raw, n := apitest.DoCounted(t, f.r, "GET", "/api/v1/budgets?status=active&sort=risk", nil)
	if status != 200 {
		t.Fatalf("list: %d %s", status, raw)
	}
	t.Logf("list of %d budgets: %d queries, %d expected-occurrence calls", len(bodies), n, calls)
	if n > 9 {
		t.Errorf("the list ran %d queries, budget 9", n)
	}
	if calls != 1 {
		t.Errorf("the list asked the Expecter %d times, want 1", calls)
	}

	calls = 0
	status, raw, n = apitest.DoCounted(t, f.r, "GET", "/api/v1/budgets/"+first, nil)
	if status != 200 {
		t.Fatalf("get: %d %s", status, raw)
	}
	t.Logf("one budget: %d queries, %d expected-occurrence calls", n, calls)
	if n > 9 {
		t.Errorf("a budget ran %d queries, budget 9", n)
	}
	if calls != 1 {
		t.Errorf("a budget asked the Expecter %d times, want 1", calls)
	}
}
