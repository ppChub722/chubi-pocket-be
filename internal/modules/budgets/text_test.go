package budgets

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — see tags/text_test.go. Budgets also got a name:
// absent / null / "" → the category's name.
func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	svc := NewService(NewStore(pool))
	cat := testdb.Category(t, pool, uid, "Food")

	b, err := svc.Create(ctx, uid, CreateRequest{
		CategoryID: cat, Amount: 5000, Period: "monthly", Scope: ScopeUser,
		Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	}, "")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if b.Name != "Food" {
		t.Errorf("create without name: name = %q, want the category's \"Food\"", b.Name)
	}
	testdb.CheckText(t, "create", b.Description, b.Note, "D1", "N1")

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"name":"Eating out","amount":6000}`
		}
		var req UpdateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := svc.Update(ctx, uid, b.ID, req, "")
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
		if got.Name != "Eating out" {
			t.Errorf("%s: name = %q, want kept \"Eating out\"", body, got.Name)
		}
	}

	var req UpdateRequest
	_ = json.Unmarshal([]byte(`{"name":null}`), &req)
	got, err := svc.Update(ctx, uid, b.ID, req, "")
	if err != nil {
		t.Fatalf("clear name: %v", err)
	}
	if got.Name != "Food" {
		t.Errorf("name null: name = %q, want the category's \"Food\"", got.Name)
	}
}
