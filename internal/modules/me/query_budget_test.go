package me

import (
	"testing"
	"time"

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
	bsvc := budgets.NewService(budgets.NewStore(pool))
	svc := NewService(pool).WithBudgets(bsvc).WithExpected(fakeExpected{})
	h, bh := NewHandler(svc), budgets.NewHandler(bsvc)
	r := apitest.CountedEngine(uid, func(g *gin.RouterGroup) {
		g.GET("/me/counts", h.Counts)
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

	status, raw, n := apitest.DoCounted(t, r, "GET", "/api/v1/me/counts", nil)
	if status != 200 {
		t.Fatalf("status %d: %s", status, raw)
	}
	t.Logf("me/counts with 4 budgets: %d queries", n)
	if n > 18 {
		t.Errorf("me/counts ran %d queries, budget 18", n)
	}
}
