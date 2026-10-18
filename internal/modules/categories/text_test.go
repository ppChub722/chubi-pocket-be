package categories

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestCreateAppendsToSiblings(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	svc := NewService(NewStore(pool))

	create := func(name, typ string, parent *Category) *Category {
		t.Helper()
		req := CreateCategoryRequest{Name: name, Type: typ}
		if parent != nil {
			req.ParentID = &parent.ID
		}
		c, err := svc.Create(ctx, uid, req)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		return c
	}
	a := create("A", "expense", nil)
	b := create("B", "expense", nil)
	inc := create("Salary", "income", nil) // another type: its own group
	child1 := create("A1", "expense", a)   // another parent: its own group
	child2 := create("A2", "expense", a)

	if a.SortOrder != 0 || b.SortOrder != 1 {
		t.Errorf("roots sort_order = %d, %d; want 0, 1", a.SortOrder, b.SortOrder)
	}
	if inc.SortOrder != 0 {
		t.Errorf("first income root sort_order = %d, want 0", inc.SortOrder)
	}
	if child1.SortOrder != 0 || child2.SortOrder != 1 {
		t.Errorf("children sort_order = %d, %d; want 0, 1", child1.SortOrder, child2.SortOrder)
	}

	if _, err := pool.Exec(ctx, `UPDATE categories SET sort_order = CASE id WHEN $1 THEN 0 ELSE 1 END
		WHERE id IN ($1, $2)`, b.ID, a.ID); err != nil {
		t.Fatal(err)
	}
	if c := create("C", "expense", nil); c.SortOrder != 2 {
		t.Errorf("after reorder: new root sort_order = %d, want 2", c.SortOrder)
	}
}

func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	svc := NewService(NewStore(pool))

	c, err := svc.Create(ctx, uid, CreateCategoryRequest{
		Name: "Text Test", Type: "expense", Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testdb.CheckText(t, "create", c.Description, c.Note, "D1", "N1")

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"include_in_report":false}`
		}
		var req UpdateCategoryRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := svc.Update(ctx, uid, c.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
	}
}
