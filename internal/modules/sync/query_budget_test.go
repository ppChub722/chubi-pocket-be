package sync

import (
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// The due check runs here once per local day; a sync that finds nothing new is a few
// queries (the budget excludes the auth middleware's one user lookup).
func TestSyncQueryBudget(t *testing.T) {
	pool := testdb.StatsPool(t)
	uid := testdb.User(t, pool)
	t.Cleanup(func() { exec(t, pool, `DELETE FROM recurring_due_checks WHERE user_id = $1`, uid) })
	h := NewHandler(pool, quietLog, testServices(pool))
	r := apitest.CountedEngine(uid, func(g *gin.RouterGroup) { g.POST("/sync", h.Sync) })

	status, raw, first := apitest.DoCounted(t, r, "POST", "/api/v1/sync", map[string]any{"have": map[string]string{}})
	if status != 200 {
		t.Fatalf("first sync: %d %s", status, raw)
	}
	var reply syncReply
	_, reply, _ = doSync(t, r, map[string]any{"have": map[string]string{}})
	have := map[string]string{}
	for name, d := range reply.Data {
		have[name] = d.V
	}
	_, _, again := apitest.DoCounted(t, r, "POST", "/api/v1/sync", map[string]any{"have": have})
	t.Logf("first sync: %d queries, nothing new: %d queries", first, again)
	if first > 31 { // 0.4.1-2 P4: + the profile dataset (GET /users/me)
		t.Errorf("a first sync ran %d queries, budget 31", first)
	}
	if again > 4 {
		t.Errorf("a sync with nothing new ran %d queries, budget 4", again)
	}
	var checks int
	if err := pool.QueryRow(t.Context(), `SELECT COUNT(*) FROM recurring_due_checks WHERE user_id = $1`, uid).Scan(&checks); err != nil || checks != 1 {
		t.Errorf("the sync did not run the day's due check: rows %d, err %v", checks, err)
	}
}
