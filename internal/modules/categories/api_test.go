package categories

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

type catJSON struct {
	ID       uuid.UUID  `json:"id"`
	Name     string     `json:"name"`
	ParentID *uuid.UUID `json:"parent_id"`
	Depth    int        `json:"depth"`
	Path     []struct {
		ID   uuid.UUID `json:"id"`
		Name string    `json:"name"`
	} `json:"path"`
	IsSystem   bool    `json:"is_system"`
	SystemKind *string `json:"system_kind"`
	SortOrder  int     `json:"sort_order"`
}

func newCatEngine(t *testing.T, uid uuid.UUID) *gin.Engine {
	t.Helper()
	pool := testdb.Pool(t)
	h := NewHandler(NewService(NewStore(pool)))
	return apitest.Engine(uid, func(g *gin.RouterGroup) {
		g.POST("/categories", h.Create)
		g.GET("/categories", h.List)
		g.PATCH("/categories/reorder", h.Reorder)
		g.GET("/categories/:id", h.Get)
		g.PUT("/categories/:id", h.Update)
		g.DELETE("/categories/:id", h.Delete)
	})
}

func mkCat(t *testing.T, r *gin.Engine, body map[string]any) catJSON {
	t.Helper()
	status, raw := apitest.Do(t, r, "POST", "/api/v1/categories", body)
	apitest.Conform(t, "createCategory", status, raw)
	if status != http.StatusCreated {
		t.Fatalf("create %v: %d %s", body, status, raw)
	}
	var c catJSON
	_ = json.Unmarshal(raw, &c)
	return c
}

// TestCategoryAPI drives every category operation and checks the responses
// against the OpenAPI spec, plus depth / path, reparenting, the fee flag.
func TestCategoryAPI(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	r := newCatEngine(t, uid)

	// createCategory: a root, a child, a grandchild; depth / path follow.
	a := mkCat(t, r, map[string]any{"name": "A", "type": "expense", "description": "D"})
	b := mkCat(t, r, map[string]any{"name": "B", "type": "expense", "parent_id": a.ID})
	c := mkCat(t, r, map[string]any{"name": "C", "type": "expense", "parent_id": b.ID})
	if a.Depth != 1 || len(a.Path) != 0 {
		t.Errorf("root = depth %d path %v, want 1 / []", a.Depth, a.Path)
	}
	if b.Depth != 2 || len(b.Path) != 1 || b.Path[0].ID != a.ID {
		t.Errorf("child = depth %d path %v, want 2 / [A]", b.Depth, b.Path)
	}
	if c.Depth != 3 || len(c.Path) != 2 || c.Path[0].Name != "A" || c.Path[1].Name != "B" {
		t.Errorf("grandchild = depth %d path %v, want 3 / [A B]", c.Depth, c.Path)
	}
	sib := mkCat(t, r, map[string]any{"name": "A2", "type": "expense"})
	if sib.SortOrder != a.SortOrder+1 {
		t.Errorf("new sibling sort_order = %d, want appended (%d)", sib.SortOrder, a.SortOrder+1)
	}

	// Errors: too deep (a 4th level), wrong-type parent, empty name.
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/categories", map[string]any{"name": "D", "type": "expense", "parent_id": c.ID}); status != http.StatusBadRequest {
		t.Errorf("4th level status = %d, want 400", status)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/categories", map[string]any{"name": "X", "type": "income", "parent_id": a.ID}); status != http.StatusBadRequest {
		t.Errorf("wrong-type parent status = %d, want 400", status)
	}
	if status, _ := apitest.Do(t, r, "POST", "/api/v1/categories", map[string]any{"name": "", "type": "expense"}); status != http.StatusBadRequest {
		t.Errorf("empty name status = %d, want 400", status)
	}

	// listCategories: every row, no status (categories are deleted, never archived).
	list := func(q string) []catJSON {
		t.Helper()
		status, raw := apitest.Do(t, r, "GET", "/api/v1/categories"+q, nil)
		apitest.Conform(t, "listCategories", status, raw)
		var out struct {
			Data []catJSON `json:"data"`
		}
		_ = json.Unmarshal(raw, &out)
		return out.Data
	}
	if got := list(""); len(got) != 4 {
		t.Errorf("list = %d rows, want 4", len(got))
	}
	// system_kind is exposed on system rows.
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := NewStore(pool).SeedForUserTx(ctx, seed, uid); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var sys *catJSON
	rows := list("?include_system=true&type=income")
	for i := range rows {
		if rows[i].IsSystem && rows[i].SystemKind != nil {
			sys = &rows[i]
		}
	}
	if sys == nil {
		t.Fatalf("no system row with system_kind in %+v", rows)
	}

	// getCategory: counts + fee flag + budget count.
	if _, err := pool.Exec(ctx, `INSERT INTO user_preferences (user_id, preferences)
		VALUES ($1, jsonb_build_object('fee_category_id', $2::text))`, uid, b.ID.String()); err != nil {
		t.Fatalf("set fee category: %v", err)
	}
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM user_preferences WHERE user_id = $1`, uid) })
	if _, err := pool.Exec(ctx, `WITH n AS (INSERT INTO budgets (id, user_id, name, amount, start_date, anchor_date)
			VALUES (gen_random_uuid(), $1, 'bud', 100, CURRENT_DATE, CURRENT_DATE) RETURNING id)
		INSERT INTO budget_categories (budget_id, category_id) SELECT id, $2 FROM n`, uid, b.ID); err != nil {
		t.Fatalf("insert budget: %v", err)
	}
	status, raw := apitest.Do(t, r, "GET", "/api/v1/categories/"+b.ID.String(), nil)
	apitest.Conform(t, "getCategory", status, raw)
	var det struct {
		catJSON
		ParentName  *string `json:"parent_name"`
		ChildCount  int     `json:"child_count"`
		BudgetCount int     `json:"budget_count"`
		IsFee       bool    `json:"is_fee_category"`
	}
	_ = json.Unmarshal(raw, &det)
	if det.ParentName == nil || *det.ParentName != "A" || det.ChildCount != 1 || !det.IsFee || det.Depth != 2 || det.BudgetCount != 1 {
		t.Errorf("detail = %+v, want parent A, 1 child, fee flag, depth 2, 1 budget", det)
	}
	status, raw = apitest.Do(t, r, "GET", "/api/v1/categories/"+a.ID.String(), nil)
	apitest.Conform(t, "getCategory", status, raw)
	det.ParentName, det.IsFee = nil, true
	_ = json.Unmarshal(raw, &det)
	if det.IsFee || det.ParentName != nil {
		t.Errorf("root detail = %+v, want no fee flag / parent", det)
	}
	if status, _ := apitest.Do(t, r, "GET", "/api/v1/categories/"+uuid.NewString(), nil); status != http.StatusNotFound {
		t.Errorf("get unknown = %d, want 404", status)
	}

	// updateCategory answers the detail.
	status, raw = apitest.Do(t, r, "PUT", "/api/v1/categories/"+a.ID.String(), map[string]any{"name": "A renamed", "note": "N"})
	apitest.Conform(t, "updateCategory", status, raw)
	_ = json.Unmarshal(raw, &det)
	if det.Name != "A renamed" || det.ChildCount != 1 {
		t.Errorf("update = %+v", det)
	}

	// reorderCategories: move C up under A (depth 2), ahead of B.
	status, raw = apitest.Do(t, r, "PATCH", "/api/v1/categories/reorder", map[string]any{"categories": []map[string]any{
		{"id": c.ID, "parent_id": a.ID, "sort_order": 0},
		{"id": b.ID, "parent_id": a.ID, "sort_order": 1},
	}})
	apitest.Conform(t, "reorderCategories", status, raw)
	var ro struct {
		Data []catJSON `json:"data"`
	}
	_ = json.Unmarshal(raw, &ro)
	var order []string
	for _, x := range ro.Data {
		if x.ParentID != nil && *x.ParentID == a.ID {
			order = append(order, x.Name)
			if x.Depth != 2 || len(x.Path) != 1 {
				t.Errorf("%s depth %d path %v, want 2 / [A renamed]", x.Name, x.Depth, x.Path)
			}
		}
	}
	if len(order) != 2 || order[0] != "C" || order[1] != "B" {
		t.Errorf("children of A in order = %v, want [C B]", order)
	}
	if status, _ := apitest.Do(t, r, "PATCH", "/api/v1/categories/reorder", map[string]any{"categories": []map[string]any{
		{"id": uuid.New(), "parent_id": nil, "sort_order": 0}}}); status != http.StatusNotFound {
		t.Errorf("reorder unknown id = %d, want 404", status)
	}

	// deleteCategory: D under B shifts up to B's parent (A).
	d := mkCat(t, r, map[string]any{"name": "D", "type": "expense", "parent_id": b.ID})
	status, raw = apitest.Do(t, r, "DELETE", "/api/v1/categories/"+b.ID.String(), nil)
	apitest.Conform(t, "deleteCategory", status, raw)
	var del struct {
		ID         uuid.UUID `json:"id"`
		Reparented []catJSON `json:"reparented"`
	}
	_ = json.Unmarshal(raw, &del)
	if del.ID != b.ID || len(del.Reparented) != 1 || del.Reparented[0].ID != d.ID ||
		del.Reparented[0].ParentID == nil || *del.Reparented[0].ParentID != a.ID ||
		del.Reparented[0].Depth != 2 || len(del.Reparented[0].Path) != 1 {
		t.Errorf("delete result = %+v, want D shifted up under A (depth 2)", del)
	}
	// A leaf: nothing moved -> reparented [] (never null).
	status, raw = apitest.Do(t, r, "DELETE", "/api/v1/categories/"+d.ID.String(), nil)
	apitest.Conform(t, "deleteCategory", status, raw)
	if status, _ := apitest.Do(t, r, "DELETE", "/api/v1/categories/"+d.ID.String(), nil); status != http.StatusNotFound {
		t.Errorf("delete twice = %d, want 404", status)
	}
	if status, _ := apitest.Do(t, r, "DELETE", "/api/v1/categories/"+sys.ID.String(), nil); status != http.StatusBadRequest {
		t.Errorf("delete system = %d, want 400", status)
	}
}
