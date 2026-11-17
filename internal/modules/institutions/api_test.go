package institutions

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

func engine(t *testing.T, uid uuid.UUID) *gin.Engine {
	t.Helper()
	h := NewHandler(NewService(testdb.Pool(t)))
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.GET("/institutions", h.List)
		g.POST("/institutions", h.Create)
		g.GET("/institutions/:id", h.Get)
		g.PUT("/institutions/:id", h.Update)
		g.DELETE("/institutions/:id", h.Delete)
		g.POST("/institutions/:id/archive", h.Archive)
		g.POST("/institutions/:id/restore", h.Restore)
	})
}

func TestInstitutionAPI(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r := engine(t, uid)
	ctx := context.Background()

	// a plain one
	status, body := apitest.Do(t, r, "POST", "/api/v1/institutions", map[string]any{"name": "  ไฟแนนซ์รถ ", "kind": "finance", "phone": "02-000-0000"})
	apitest.Conform(t, "createInstitution", status, body)
	if status != http.StatusCreated {
		t.Fatalf("create = %d %s", status, body)
	}
	var car Institution
	_ = json.Unmarshal(body, &car)
	if car.Name != "ไฟแนนซ์รถ" || car.Kind != "finance" || car.Provider != nil || car.Status != "active" {
		t.Errorf("created = %+v", car)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/institutions", map[string]any{"name": "x", "kind": "person"}); status != http.StatusBadRequest {
		t.Errorf("bad kind = %d, want 400", status)
	}

	// from a central provider: defaults from it, then the same row again (200)
	var ktc uuid.UUID
	if err := pool.QueryRow(ctx, `SELECT id FROM payment_providers WHERE scheme = 'chubi' AND code = 'ktc'`).Scan(&ktc); err != nil {
		t.Fatalf("ktc provider: %v", err)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/institutions", map[string]any{"provider_id": ktc})
	apitest.Conform(t, "createInstitution", status, body)
	var card Institution
	_ = json.Unmarshal(body, &card)
	if status != http.StatusCreated || card.Kind != "card" || card.Provider == nil || card.Provider.ShortName == nil || *card.Provider.ShortName != "KTC" ||
		card.Phone == nil || *card.Phone == "" {
		t.Errorf("provider create = %d %+v", status, card)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/institutions", map[string]any{"provider_id": ktc, "name": "again"})
	apitest.Conform(t, "createInstitution", status, body)
	var again Institution
	_ = json.Unmarshal(body, &again)
	if status != http.StatusOK || again.ID != card.ID || again.Name == "again" {
		t.Errorf("second provider create = %d %+v, want 200 with the first row", status, again)
	}

	// a wallet and a debt row point at it
	acc := testdb.Account(t, pool, uid)
	if _, err := pool.Exec(ctx, `UPDATE accounts SET institution_id = $1 WHERE id = $2`, card.ID, acc); err != nil {
		t.Fatal(err)
	}
	debtID, _ := uuid.NewV7()
	if _, err := pool.Exec(ctx, `INSERT INTO personal_debts (id, user_id, direction, counterparty_institution_id, counterparty_person_name,
		amount, currency, date) VALUES ($1, $2, 'received', $3, $4, 9000, 'THB', CURRENT_DATE)`, debtID, uid, card.ID, card.Name); err != nil {
		t.Fatal(err)
	}
	status, body = apitest.Do(t, r, "GET", "/api/v1/institutions/"+card.ID.String(), nil)
	apitest.Conform(t, "getInstitution", status, body)
	var d Detail
	_ = json.Unmarshal(body, &d)
	if len(d.Accounts) != 1 || d.Accounts[0].ID != acc || d.Debt.Key != "i:"+card.ID.String() || d.Debt.Net != -9000 || d.Debt.Side != "payable" || d.Debt.EntryCount != 1 {
		t.Errorf("detail = %+v", d)
	}

	// edit renames the debt rows' display copy
	status, body = apitest.Do(t, r, "PUT", "/api/v1/institutions/"+card.ID.String(), map[string]any{"name": "KTC Visa", "kind": "card", "note": "N"})
	apitest.Conform(t, "updateInstitution", status, body)
	var snap string
	_ = pool.QueryRow(ctx, `SELECT counterparty_person_name FROM personal_debts WHERE id = $1`, debtID).Scan(&snap)
	if status != http.StatusOK || snap != "KTC Visa" {
		t.Errorf("update = %d, debt name %q", status, snap)
	}

	// in use: no delete; archive / restore
	status, body = apitest.Do(t, r, "DELETE", "/api/v1/institutions/"+card.ID.String(), nil)
	apitest.Conform(t, "deleteInstitution", status, body)
	if status != http.StatusConflict {
		t.Errorf("delete in use = %d, want 409", status)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/institutions/"+card.ID.String()+"/archive", nil)
	apitest.Conform(t, "archiveInstitution", status, body)
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/institutions/"+card.ID.String()+"/archive", nil); status != http.StatusConflict {
		t.Errorf("archive twice = %d, want 409", status)
	}
	status, body = apitest.Do(t, r, "GET", "/api/v1/institutions", nil)
	apitest.Conform(t, "listInstitutions", status, body)
	var list List
	_ = json.Unmarshal(body, &list)
	if len(list.Data) != 1 || list.Data[0].ID != car.ID {
		t.Errorf("active list = %+v, want the finance one only", list.Data)
	}
	status, body = apitest.Do(t, r, "POST", "/api/v1/institutions/"+card.ID.String()+"/restore", nil)
	apitest.Conform(t, "restoreInstitution", status, body)

	// free: delete; another user's: 404
	status, body = apitest.Do(t, r, "DELETE", "/api/v1/institutions/"+car.ID.String(), nil)
	apitest.Conform(t, "deleteInstitution", status, body)
	if status != http.StatusOK {
		t.Errorf("delete = %d %s", status, body)
	}
	other := engine(t, testdb.User(t, pool))
	status, body = apitest.Do(t, other, "GET", "/api/v1/institutions/"+card.ID.String(), nil)
	apitest.Conform(t, "getInstitution", status, body)
	if status != http.StatusNotFound {
		t.Errorf("other user's = %d, want 404", status)
	}
}
