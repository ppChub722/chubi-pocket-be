package accounts

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/notifications"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// GET /accounts/:id/summary answers for a member and not for anyone else; the wallet
// page reads it for a month other than this one. The wallet detail loads the wallet once
// (the budget excludes the auth middleware's one user lookup).
func TestAccountSummaryRouteAndDetailQueryBudget(t *testing.T) {
	pool := testdb.StatsPool(t)
	cats := categories.NewService(categories.NewStore(pool))
	svc := NewService(NewStore(pool), transactions.NewService(transactions.NewStore(pool), cats), cats)
	svc.WithNotificationService(notifications.NewService(notifications.NewStore(pool)))
	h := NewHandler(svc)
	uid, other := testdb.User(t, pool), testdb.User(t, pool)
	acc := testdb.Account(t, pool, uid)
	mine := apitest.CountedEngine(uid, func(g *gin.RouterGroup) {
		g.GET("/accounts/:id", h.Get)
		g.GET("/accounts/:id/summary", h.Summary)
	})
	theirs := apitest.CountedEngine(other, func(g *gin.RouterGroup) { g.GET("/accounts/:id/summary", h.Summary) })

	status, raw, n := apitest.DoCounted(t, mine, "GET", "/api/v1/accounts/"+acc.String()+"/summary?from=2026-09-01&to=2026-09-30", nil)
	if status != http.StatusOK {
		t.Fatalf("summary: %d %s", status, raw)
	}
	apitest.Conform(t, "getAccountSummary", status, raw)
	t.Logf("summary: %d queries", n)
	if n > 2 {
		t.Errorf("the summary ran %d queries, budget 2", n)
	}
	if status, _, _ := apitest.DoCounted(t, theirs, "GET", "/api/v1/accounts/"+acc.String()+"/summary", nil); status != http.StatusNotFound {
		t.Errorf("a non-member got %d, want 404", status)
	}
	if status, _, _ := apitest.DoCounted(t, mine, "GET", "/api/v1/accounts/"+acc.String()+"/summary?from=nope", nil); status != http.StatusBadRequest {
		t.Errorf("a bad date got %d, want 400", status)
	}

	status, raw, n = apitest.DoCounted(t, mine, "GET", "/api/v1/accounts/"+acc.String()+"?include=summary,members", nil)
	if status != http.StatusOK {
		t.Fatalf("detail: %d %s", status, raw)
	}
	t.Logf("wallet detail: %d queries", n)
	if n > 8 {
		t.Errorf("the wallet detail ran %d queries, budget 8", n)
	}
}
