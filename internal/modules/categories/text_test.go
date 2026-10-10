package categories

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — see tags/text_test.go.
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
