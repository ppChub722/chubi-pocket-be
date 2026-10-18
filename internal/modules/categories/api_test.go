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

func TestCategoryAPI(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	r := newCatEngine(t, uid)

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

	status, raw = apitest.Do(t, r, "PUT", "/api/v1/categories/"+a.ID.String(), map[string]any{"name": "A renamed", "note": "N"})
	apitest.Conform(t, "updateCategory", status, raw)
	_ = json.Unmarshal(raw, &det)
	if det.Name != "A renamed" || det.ChildCount != 1 {
		t.Errorf("update = %+v", det)
	}

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

// TestCategoryMonthNumbers — month_spent / month_count: a parent counts its sub-categories'
// transactions too; last month and other types don't count.
func TestCategoryMonthNumbers(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	r := newCatEngine(t, uid)
	food := mkCat(t, r, map[string]any{"name": "Food", "type": "expense"})
	lunch := mkCat(t, r, map[string]any{"name": "Lunch", "type": "expense", "parent_id": food.ID})
	other := mkCat(t, r, map[string]any{"name": "Other", "type": "expense"})
	ins := func(cat uuid.UUID, amount float64, daysAgo int) {
		if _, err := pool.Exec(ctx, `INSERT INTO transactions (id, user_id, type, amount, date, category_id)
			VALUES (gen_random_uuid(), $1, 'expense', $2, CURRENT_DATE - $3::int, $4)`, uid, amount, daysAgo, cat); err != nil {
			t.Fatal(err)
		}
	}
	ins(food.ID, 100, 0)
	ins(lunch.ID, 50, 0)
	ins(lunch.ID, 25, 0)
	ins(other.ID, 7, 0)
	ins(lunch.ID, 999, 70) // two months back: never this month
	status, raw := apitest.Do(t, r, "GET", "/api/v1/categories", nil)
	apitest.Conform(t, "listCategories", status, raw)
	var out struct {
		Data []struct {
			ID    uuid.UUID `json:"id"`
			Spent float64   `json:"month_spent"`
			Count int       `json:"month_count"`
		} `json:"data"`
	}
	_ = json.Unmarshal(raw, &out)
	got := map[uuid.UUID][2]float64{}
	for _, c := range out.Data {
		got[c.ID] = [2]float64{c.Spent, float64(c.Count)}
	}
	if got[food.ID] != [2]float64{175, 3} || got[lunch.ID] != [2]float64{75, 2} || got[other.ID] != [2]float64{7, 1} {
		t.Errorf("month numbers = %v (food %v lunch %v other %v)", got, food.ID, lunch.ID, other.ID)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM transactions WHERE user_id = $1`, uid); err != nil {
		t.Fatal(err)
	}
}

func TestCategoryDuplicateNameAndPath(t *testing.T) {
	pool := testdb.Pool(t)
	uid := testdb.User(t, pool)
	r := newCatEngine(t, uid)
	root := mkCat(t, r, map[string]any{"name": "Home", "type": "expense",
		"icon_code": map[string]any{"icon": "home", "iconColors": []string{"#FFFFFF"}, "background": "solid", "bgColors": []string{"#112233"}, "border": "none", "borderColors": []string{}, "shape": "circle"}})
	child := mkCat(t, r, map[string]any{"name": "Rent", "type": "expense", "parent_id": root.ID})

	for _, name := range []string{"Rent", "rent"} {
		status, raw := apitest.Do(t, r, "POST", "/api/v1/categories", map[string]any{"name": name, "type": "expense", "parent_id": root.ID})
		var e struct {
			Error struct {
				Code    string            `json:"code"`
				Details map[string]string `json:"details"`
			} `json:"error"`
		}
		_ = json.Unmarshal(raw, &e)
		if status != http.StatusUnprocessableEntity || e.Error.Code != "CATEGORY_NAME_DUPLICATE" || e.Error.Details["field"] != "name" {
			t.Errorf("create %q: %d %s", name, status, raw)
		}
	}
	other := mkCat(t, r, map[string]any{"name": "Other", "type": "expense", "parent_id": root.ID})
	status, raw := apitest.Do(t, r, "PUT", "/api/v1/categories/"+other.ID.String(), map[string]any{"name": "RENT"})
	if status != http.StatusUnprocessableEntity {
		t.Errorf("rename onto a sibling: %d %s", status, raw)
	}

	status, raw = apitest.Do(t, r, "GET", "/api/v1/categories/"+child.ID.String(), nil)
	apitest.Conform(t, "getCategory", status, raw)
	var det struct {
		Path []struct {
			ID       uuid.UUID       `json:"id"`
			Name     string          `json:"name"`
			IconCode json.RawMessage `json:"icon_code"`
		} `json:"path"`
	}
	_ = json.Unmarshal(raw, &det)
	if len(det.Path) != 1 || det.Path[0].ID != root.ID || det.Path[0].Name != "Home" || string(det.Path[0].IconCode) == "null" || len(det.Path[0].IconCode) == 0 {
		t.Errorf("path = %s", raw)
	}
}
