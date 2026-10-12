package scheduled_transactions

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/ppChub722/chubi-pocket-be/internal/apidoc/apitest"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestScheduledSharedRefsAndGenerateNow(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	me := testdb.User(t, pool)
	wallet := testdb.Account(t, pool, me)
	if _, err := pool.Exec(ctx, `UPDATE accounts SET name = 'Main', balance = 1000 WHERE id = $1`, wallet); err != nil {
		t.Fatal(err)
	}
	cat := testdb.Category(t, pool, me, "Bills")
	h := NewHandler(NewService(NewStore(pool)))
	r := apitest.Engine(me, func(g *gin.RouterGroup) {
		g.POST("/scheduled-transactions", h.Create)
		g.GET("/scheduled-transactions", h.List)
		g.GET("/scheduled-transactions/:id", h.Get)
		g.POST("/scheduled-transactions/:id/pause", h.Pause)
		g.POST("/scheduled-transactions/:id/generate-now", h.GenerateNow)
	})

	status, body := apitest.Do(t, r, "POST", "/api/v1/scheduled-transactions", map[string]any{
		"name": "Rent", "type": "expense", "entry_type": "recurring", "amount": 300,
		"account_id": wallet, "category_id": cat, "billing_cycle": "monthly", "next_billing_date": "2026-11-01",
	})
	if status != http.StatusCreated {
		t.Fatalf("create: %d %s", status, body)
	}
	var created map[string]any
	_ = json.Unmarshal(body, &created)
	id := created["id"].(string)

	check := func(label string, raw []byte) {
		t.Helper()
		var s map[string]any
		_ = json.Unmarshal(raw, &s)
		acc, _ := s["account"].(map[string]any)
		cat2, _ := s["category"].(map[string]any)
		if acc["id"] != wallet.String() || acc["name"] != "Main" || acc["group"] != "asset" || acc["currency"] != "THB" {
			t.Errorf("%s: account = %v, want the shared AccountRef", label, acc)
		}
		if cat2["id"] != cat.String() || cat2["type"] != "expense" {
			t.Errorf("%s: category = %v, want the shared CategoryRef", label, cat2)
		}
	}
	check("create", body)

	status, body = apitest.Do(t, r, "GET", "/api/v1/scheduled-transactions", nil)
	apitest.Conform(t, "listScheduled", status, body)
	var list struct {
		Data []json.RawMessage `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Data) != 1 {
		t.Fatalf("list = %d, want 1", len(list.Data))
	}
	check("list", list.Data[0])
	status, body = apitest.Do(t, r, "GET", "/api/v1/scheduled-transactions/"+id, nil)
	check("get", body)
	status, body = apitest.Do(t, r, "POST", "/api/v1/scheduled-transactions/"+id+"/pause", nil)
	if status != http.StatusOK {
		t.Fatalf("pause: %d %s", status, body)
	}
	check("pause", body)

	// generate-now on an active schedule: money moves, the response carries
	// the post-commit wallet balance.
	if _, err := pool.Exec(ctx, `UPDATE scheduled_transactions SET status = 'active' WHERE id = $1`, id); err != nil {
		t.Fatal(err)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/scheduled-transactions/"+id+"/generate-now", nil)
	if status != http.StatusOK {
		t.Fatalf("generate-now: %d %s", status, body)
	}
	apitest.Conform(t, "generateScheduledNow", status, body)
	var gen struct {
		AccountBalances []struct {
			AccountID string  `json:"account_id"`
			Balance   float64 `json:"balance"`
		} `json:"account_balances"`
	}
	_ = json.Unmarshal(body, &gen)
	if len(gen.AccountBalances) != 1 || gen.AccountBalances[0].AccountID != wallet.String() || gen.AccountBalances[0].Balance != 700 {
		t.Errorf("account_balances = %+v, want the wallet at 700", gen.AccountBalances)
	}
}
