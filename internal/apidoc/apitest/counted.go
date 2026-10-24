package apitest

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/database"
	"github.com/ppChub722/chubi-pocket-be/internal/shared"
)

// CountedEngine is Engine plus what main.go's protected group adds to the context
// (the per-request user preferences cache), so the SQL a handler runs here is the SQL it
// runs for real. The auth middleware's one user lookup is not in it.
func CountedEngine(userID uuid.UUID, register func(g *gin.RouterGroup)) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	g := r.Group("/api/v1")
	g.Use(func(c *gin.Context) {
		c.Set(userKey, userID)
		c.Request = c.Request.WithContext(shared.WithUserPrefs(c.Request.Context()))
		c.Next()
	})
	register(g)
	return r
}

// DoCounted runs one request and returns how many SQL statements it ran. The pool must
// come from testdb.StatsPool.
func DoCounted(t *testing.T, r http.Handler, method, path string, body any) (int, []byte, int64) {
	t.Helper()
	var rd io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("encode body: %v", err)
		}
		rd = bytes.NewReader(raw)
	}
	req := httptest.NewRequest(method, path, rd)
	ctx, stats := database.WithQueryStats(req.Context())
	req = req.WithContext(ctx)
	if rd != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code, w.Body.Bytes(), stats.Count()
}
