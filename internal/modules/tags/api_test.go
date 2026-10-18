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

	if status, _ := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": "coffee"}); status != http.StatusConflict {
		t.Errorf("duplicate create status = %d, want 409", status)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": ""}); status != http.StatusBadRequest {
		t.Errorf("empty name status = %d, want 400", status)
	}

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

func TestTagBatch(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r, _ := tagEngine(t, uid)
	ctx := context.Background()

	keep := tagOf(t, r, "keep")
	gone := tagOf(t, r, "gone")
	rename := tagOf(t, r, "rename-me")

	type batchOut struct {
		Created []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"created"`
		Updated []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"updated"`
		Deleted []uuid.UUID `json:"deleted"`
		Data    []struct {
			ID   uuid.UUID `json:"id"`
			Name string    `json:"name"`
		} `json:"data"`
	}

	status, body := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{
		"delete": []string{gone.String()},
		"update": []map[string]any{{"id": rename.String(), "name": "  renamed  "}},
		"create": []map[string]any{{"name": "  first  "}, {"name": "second", "note": "N"}},
	})
	apitest.Conform(t, "createTag", status, body)
	if status != http.StatusOK {
		t.Fatalf("batch status %d: %s", status, body)
	}
	var out batchOut
	_ = json.Unmarshal(body, &out)
	if len(out.Created) != 2 || out.Created[0].Name != "first" || out.Created[1].Name != "second" {
		t.Errorf("created = %+v, want first, second in request order", out.Created)
	}
	if len(out.Updated) != 1 || out.Updated[0].ID != rename || out.Updated[0].Name != "renamed" {
		t.Errorf("updated = %+v, want the renamed tag, trimmed", out.Updated)
	}
	if len(out.Deleted) != 1 || out.Deleted[0] != gone {
		t.Errorf("deleted = %v, want [%s]", out.Deleted, gone)
	}
	names := map[string]bool{}
	for _, d := range out.Data {
		names[d.Name] = true
	}
	if len(out.Data) != 4 || !names["keep"] || !names["renamed"] || !names["first"] || !names["second"] || names["gone"] {
		t.Errorf("data = %+v, want keep, renamed, first, second", out.Data)
	}

	// An update without its other fields leaves them (note stays).
	var note *string
	_ = pool.QueryRow(ctx, `SELECT note FROM tags WHERE id = $1`, out.Created[1].ID).Scan(&note)
	if note == nil || *note != "N" {
		t.Errorf("created note = %v, want N", note)
	}

	status, body = apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{
		"delete": []string{keep.String()},
		"update": []map[string]any{{"id": rename.String(), "name": "renamed-again"}},
		"create": []map[string]any{{"name": "fresh"}, {"name": "second"}},
	})
	if status != http.StatusConflict {
		t.Fatalf("clashing batch status = %d, want 409: %s", status, body)
	}
	var fail struct {
		Error struct {
			Code    string `json:"code"`
			Details struct {
				Op    string `json:"op"`
				Index int    `json:"index"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &fail)
	if fail.Error.Code != "TAG_EXISTS" || fail.Error.Details.Op != "create" || fail.Error.Details.Index != 1 {
		t.Errorf("error = %+v, want TAG_EXISTS at create #1", fail.Error)
	}
	var n int
	_ = pool.QueryRow(ctx, `SELECT COUNT(*) FROM tags WHERE user_id = $1`, uid).Scan(&n)
	if n != 4 {
		t.Errorf("tags after a failed batch = %d, want 4 (nothing applied)", n)
	}
	var nm string
	_ = pool.QueryRow(ctx, `SELECT name FROM tags WHERE id = $1`, rename).Scan(&nm)
	if nm != "renamed" {
		t.Errorf("name after a failed batch = %q, want renamed", nm)
	}

	if status, _ := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{
		"update": []map[string]any{{"id": uuid.NewString(), "name": "x"}},
	}); status != http.StatusNotFound {
		t.Errorf("unknown id status = %d, want 404", status)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{
		"delete": []string{keep.String()},
		"update": []map[string]any{{"id": keep.String(), "name": "x"}},
	}); status != http.StatusBadRequest {
		t.Errorf("id in delete and update status = %d, want 400", status)
	}

	status, body = apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"create": []any{}})
	if status != http.StatusOK {
		t.Fatalf("empty batch status = %d: %s", status, body)
	}
	var empty batchOut
	_ = json.Unmarshal(body, &empty)
	if len(empty.Data) != 4 || len(empty.Created) != 0 {
		t.Errorf("empty batch = %+v, want the 4 tags and none created", empty)
	}

	status, body = apitest.Do(t, r, "POST", "/api/v1/tags", map[string]any{"name": "solo"})
	apitest.Conform(t, "createTag", status, body)
	if status != http.StatusCreated {
		t.Errorf("single create status = %d, want 201", status)
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
