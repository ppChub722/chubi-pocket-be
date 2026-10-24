package notifications

import (
	"context"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// The badge poll and the inbox read stay a few queries, and never run the recurring
// due check (the budget excludes the auth middleware's one user lookup).
func TestNotificationReadsQueryBudget(t *testing.T) {
	pool := testdb.StatsPool(t)
	uid := testdb.User(t, pool)
	if _, err := pool.Exec(context.Background(),
		`INSERT INTO user_notification_settings (user_id) VALUES ($1)`, uid); err != nil {
		t.Fatalf("seed settings: %v", err)
	}
	h := NewHandler(NewService(NewStore(pool)))
	r := apitest.CountedEngine(uid, func(g *gin.RouterGroup) {
		g.GET("/notifications", h.List)
		g.GET("/notifications/unread-count", h.UnreadCount)
	})
	for _, c := range []struct {
		name, path string
		max        int64
	}{
		{"unread count", "/notifications/unread-count", 1},
		{"list", "/notifications?page=1&per_page=20", 4},
	} {
		status, raw, n := apitest.DoCounted(t, r, "GET", "/api/v1"+c.path, nil)
		if status != 200 {
			t.Fatalf("%s: status %d: %s", c.name, status, raw)
		}
		t.Logf("%s: %d queries", c.name, n)
		if n > c.max {
			t.Errorf("%s ran %d queries, budget %d", c.name, n, c.max)
		}
	}
}
