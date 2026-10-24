package dashboard

import (
	"testing"

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
	svc := NewService(NewStore(pool), txSvc, accSvc, budgets.NewService(budgets.NewStore(pool)),
		saving_goals.NewService(saving_goals.NewStore(pool)), pdSvc)
	h := NewHandler(svc)
	r := apitest.CountedEngine(uid, func(g *gin.RouterGroup) { g.GET("/dashboard", h.Get) })

	status, raw, n := apitest.DoCounted(t, r, "GET", "/api/v1/dashboard", nil)
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	t.Logf("dashboard without budgets: %d queries", n)
	if n > 27 {
		t.Errorf("dashboard ran %d queries, budget 27", n)
	}
}
