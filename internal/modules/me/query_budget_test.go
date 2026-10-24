package me

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/budgets"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// The counts never run the recurring due check, and the timezone / language are read
// once. The budget part is the budgets package's own ratchet (the auth middleware's one
// user lookup is not counted here).
func TestMyCountsQueryBudget(t *testing.T) {
	pool := testdb.StatsPool(t)
	uid := testdb.User(t, pool)
	svc := NewService(pool).WithBudgets(budgets.NewService(budgets.NewStore(pool))).WithExpected(fakeExpected{})
	h := NewHandler(svc)
	r := apitest.CountedEngine(uid, func(g *gin.RouterGroup) { g.GET("/me/counts", h.Counts) })

	status, raw, n := apitest.DoCounted(t, r, "GET", "/api/v1/me/counts", nil)
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	t.Logf("me/counts without budgets: %d queries", n)
	if n > 15 {
		t.Errorf("me/counts ran %d queries, budget 15", n)
	}
}
