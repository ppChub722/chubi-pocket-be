package tags

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — a partial update without description / note
// keeps them; null or "" clears (owner standard 2026-10-10).
func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	svc := NewService(NewStore(pool))

	tag, err := svc.Create(ctx, uid, CreateTagRequest{
		Name: "text-test", Description: testdb.Str(" D1 "), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testdb.CheckText(t, "create", tag.Description, tag.Note, "D1", "N1")

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"name":"text-test-2"}`
		}
		var req UpdateTagRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := svc.Update(ctx, uid, tag.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
	}
}
