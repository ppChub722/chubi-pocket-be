package tags

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

func tagEngine(t *testing.T, uid uuid.UUID) (*gin.Engine, *Service) {
	t.Helper()
	pool := testdb.Pool(t)
	svc := NewService(NewStore(pool))
	h := NewHandler(svc)
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.POST("/tags", h.Create)
		g.GET("/tags", h.List)
		g.PUT("/tags/:id", h.Update)
		g.DELETE("/tags/:id", h.Delete)
	}), svc
}

// TestTagAPI drives every tag operation over HTTP and checks each response
// against the OpenAPI spec (listTags, createTag, updateTag, deleteTag).
func TestTagAPI(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r, _ := tagEngine(t, uid)
	ctx := context.Background()

	status, body := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": "  coffee  ", "description": "D", "note": "N"})
	apitest.Conform(t, "createTag", status, body)
	if status != http.StatusCreated {
		t.Fatalf("create status %d: %s", status, body)
	}
	var created struct {
		ID         uuid.UUID `json:"id"`
		Name       string    `json:"name"`
		UsageCount int       `json:"usage_count"`
	}
	_ = json.Unmarshal(body, &created)
	if created.Name != "coffee" || created.UsageCount != 0 {
		t.Errorf("created = %+v, want trimmed name and usage 0", created)
	}

	// Duplicate name -> 409 TAG_EXISTS; empty name -> 400.
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": "coffee"}); status != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409", status)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": ""}); status != http.StatusBadRequest {
		t.Errorf("empty name status = %d, want 400", status)
	}

	// A second tag used by a transaction: list orders by usage then name.
	other := tagOf(t, r, "tea")
	acc := testdb.Account(t, pool, uid)
	txID := uuid.New()
	if _, err := pool.Exec(ctx, `INSERT INTO transactions (id, user_id, account_id, type, amount, date)
		VALUES ($1, $2, $3, 'expense', 10, CURRENT_DATE)`, txID, uid, acc); err != nil {
		t.Fatalf("insert tx: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO transaction_tags (transaction_id, tag_id, created_by_user_id)
		VALUES ($1, $2, $3)`, txID, other, uid); err != nil {
		t.Fatalf("attach: %v", err)
	}

	status, body = apitest.Do(t, r, "GET", "/api/v1/tags", nil)
	apitest.Conform(t, "listTags", status, body)
	var list struct {
		Data []struct {
			Name       string `json:"name"`
			UsageCount int    `json:"usage_count"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &list)
	if len(list.Data) != 2 || list.Data[0].Name != "tea" || list.Data[0].UsageCount != 1 || list.Data[1].Name != "coffee" {
		t.Errorf("list = %+v, want tea (1 use) first, then coffee", list.Data)
	}

	status, body = apitest.Do(t, r, "PUT", "/api/v1/tags/"+created.ID.String(), map[string]any{"name": "espresso"})
	apitest.Conform(t, "updateTag", status, body)
	var upd struct {
		Name string  `json:"name"`
		Note *string `json:"note"`
	}
	_ = json.Unmarshal(body, &upd)
	if upd.Name != "espresso" || upd.Note == nil || *upd.Note != "N" {
		t.Errorf("update = %+v, want renamed and note kept", upd)
	}
	if status, _ := apitest.Do(t, r, "PUT", "/api/v1/tags/"+uuid.NewString(), map[string]any{"name": "x"}); status != http.StatusNotFound {
		t.Errorf("update unknown status = %d, want 404", status)
	}

	// Delete removes it from transactions too and answers {id}.
	status, body = apitest.Do(t, r, "DELETE", "/api/v1/tags/"+other.String(), nil)
	apitest.Conform(t, "deleteTag", status, body)
	var del struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(body, &del)
	if del.ID != other {
		t.Errorf("delete id = %s, want %s", del.ID, other)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM transaction_tags WHERE tag_id = $1`, other).Scan(&n)
	if n != 0 {
		t.Errorf("transaction_tags left = %d, want 0", n)
	}
	if status, _ := apitest.Do(t, r, "DELETE", "/api/v1/tags/"+other.String(), nil); status != http.StatusNotFound {
		t.Errorf("delete twice status = %d, want 404", status)
	}
}

func tagOf(t *testing.T, r *gin.Engine, name string) uuid.UUID {
	t.Helper()
	status, body := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": name})
	if status != http.StatusCreated {
		t.Fatalf("create %s: %d %s", name, status, body)
	}
	var out struct {
		ID uuid.UUID `json:"id"`
	}
	_ = json.Unmarshal(body, &out)
	return out.ID
}
