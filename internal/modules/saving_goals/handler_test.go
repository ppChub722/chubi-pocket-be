package saving_goals

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestSavingGoalsSharedAccountRef(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	me := testdb.User(t, pool)
	wallet := testdb.Account(t, pool, me)
	if _, err := pool.Exec(ctx, `UPDATE accounts SET name = 'Savings', balance = 1000 WHERE id = $1`, wallet); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(NewService(NewStore(pool)))
	r := apitest.Engine(me, func(g *gin.RouterGroup) {
		g.POST("/saving-goals", h.Create)
		g.GET("/saving-goals", h.List)
		g.GET("/saving-goals/:id", h.Get)
		g.POST("/saving-goals/:id/archive", h.Archive)
	})

	status, body := apitest.Do(t, r, "POST", "/api/v1/saving-goals", map[string]any{
		"name": "Trip", "target_amount": 1000, "linked_account_id": wallet, "allocation_pct": 50,
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	id := created["id"].(string)

	check := func(label string, raw []byte) {
		t.Helper()
		var g map[string]any
		if err := json.Unmarshal(raw, &g); err != nil {
			t.Fatalf("%s: %v", label, err)
		}
		acc, _ := g["linked_account"].(map[string]any)
		if acc["id"] != wallet.String() || acc["name"] != "Savings" || acc["group"] != "asset" {
			t.Errorf("%s: linked_account = %v, want the shared AccountRef of the wallet", label, acc)
		}
		if _, old := acc["balance"]; old {
			t.Errorf("%s: linked_account still carries balance", label)
		}
		if g["linked_account_balance"] != float64(1000) || g["current_amount"] != float64(500) {
			t.Errorf("%s: linked_account_balance=%v current_amount=%v, want 1000 / 500", label, g["linked_account_balance"], g["current_amount"])
		}
	}
	check("create", body)

	status, body = apitest.Do(t, r, "GET", "/api/v1/saving-goals/"+id, nil)
	if status != http.StatusOK {
		t.Fatalf("get: %d %s", status, body)
	}
	apitest.Conform(t, "getSavingGoal", status, body)
	check("get", body)

	status, body = apitest.Do(t, r, "GET", "/api/v1/saving-goals", nil)
	apitest.Conform(t, "listSavingGoals", status, body)
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Data) != 1 {
		t.Fatalf("list = %d goals, want 1", len(list.Data))
	}
	check("list", list.Data[0])

	status, body = apitest.Do(t, r, "POST", "/api/v1/saving-goals/"+id+"/archive", nil)
	if status != http.StatusOK {
		t.Fatalf("archive: %d %s", status, body)
	}
	check("archive", body)

	// The balance moves: the goal follows the live value.
	if _, err := pool.Exec(ctx, `UPDATE accounts SET balance = 400 WHERE id = $1`, wallet); err != nil {
		t.Fatal(err)
	}
	status, body = apitest.Do(t, r, "GET", "/api/v1/saving-goals/"+id, nil)
	var g map[string]any
	_ = json.Unmarshal(body, &g)
	if g["linked_account_balance"] != float64(400) || g["current_amount"] != float64(200) {
		t.Errorf("after balance change: %v / %v, want 400 / 200", g["linked_account_balance"], g["current_amount"])
	}
	_ = uuid.Nil
}
