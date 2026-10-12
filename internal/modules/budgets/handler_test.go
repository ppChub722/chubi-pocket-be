package budgets

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestBudgetsSharedCategoryRef(t *testing.T) {
	pool := testdb.Pool(t)
	me := testdb.User(t, pool)
	food := testdb.Category(t, pool, me, "Food")
	h := NewHandler(NewService(NewStore(pool)))
	r := apitest.Engine(me, func(g *gin.RouterGroup) {
		g.POST("/budgets", h.Create)
		g.GET("/budgets", h.List)
		g.GET("/budgets/:id", h.Get)
	})

	status, body := apitest.Do(t, r, "POST", "/api/v1/budgets", map[string]any{
		"category_id": food, "amount": 500, "period": "monthly", "scope": "user",
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	check := func(label string, raw []byte) {
		t.Helper()
		var b map[string]any
		if err := json.Unmarshal(raw, &b); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		c, _ := b["category"].(map[string]any)
		if c["id"] != food.String() || c["name"] != "Food" || c["type"] != "expense" || c["is_system"] != false {
			t.Errorf("%s: category = %v, want the shared CategoryRef", label, c)
		}
		for _, k := range []string{"system_kind", "parent_id", "icon_code"} {
			if _, ok := c[k]; !ok {
				t.Errorf("%s: category misses key %s", label, k)
			}
		}
	}
	check("create", body)
	var created map[string]any
	_ = json.Unmarshal(body, &created)

	status, body = apitest.Do(t, r, "GET", "/api/v1/budgets", nil)
	apitest.Conform(t, "listBudgets", status, body)
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Data) != 1 {
		t.Fatalf("list = %d budgets, want 1", len(list.Data))
	}
	check("list", list.Data[0])

	status, body = apitest.Do(t, r, "GET", "/api/v1/budgets/"+created["id"].(string), nil)
	if status != http.StatusOK {
		t.Fatalf("get: %d %s", status, body)
	}
	check("get", body)
}
