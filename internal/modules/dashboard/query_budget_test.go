package dashboard

import (
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/accounts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/personal_debts"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/saving_goals"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// The dashboard never runs the recurring due check, and the timezone / language are read
// once. The budget part is the budgets package's own ratchet (the auth middleware's one
// user lookup is not counted here).
func TestDashboardQueryBudget(t *testing.T) {
	pool := testdb.StatsPool(t)
	uid := testdb.User(t, pool)
	testdb.Account(t, pool, uid)
	catSvc := categories.NewService(categories.NewStore(pool))
	txSvc := transactions.NewService(transactions.NewStore(pool), catSvc)
	accSvc := accounts.NewService(accounts.NewStore(pool), txSvc, catSvc)
	pdSvc := personal_debts.NewService(personal_debts.NewStore(pool), txSvc)
	bsvc := budgets.NewService(budgets.NewStore(pool))
	svc := NewService(NewStore(pool), txSvc, accSvc, bsvc, saving_goals.NewService(saving_goals.NewStore(pool)), pdSvc)
	h, bh := NewHandler(svc), budgets.NewHandler(bsvc)
	r := apitest.CountedEngine(uid, func(g *gin.RouterGroup) {
		g.GET("/dashboard", h.Get)
		g.POST("/budgets", bh.Create)
	})
	t.Cleanup(func() { _, _ = pool.Exec(t.Context(), `DELETE FROM budgets WHERE user_id = $1`, uid) })
	for i := 0; i < 4; i++ {
		body := map[string]any{"name": "B" + string(rune('A'+i)), "amount": 1000, "recurring": true,
			"rule": map[string]any{"freq": "monthly"}, "start_date": time.Now().AddDate(0, -2, 0).Format("2006-01-02"),
			"scope": map[string]any{"all": true}}
		if status, raw := apitest.Do(t, r, "POST", "/api/v1/budgets", body); status != 201 {
			t.Fatalf("create budget: %d %s", status, raw)
		}
	}

	status, raw, n := apitest.DoCounted(t, r, "GET", "/api/v1/dashboard", nil)
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	t.Logf("dashboard with 4 budgets: %d queries", n)
	if n > 22 {
		t.Errorf("dashboard ran %d queries, budget 22", n)
	}
}
