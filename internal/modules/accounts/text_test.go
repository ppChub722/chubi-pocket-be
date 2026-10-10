package accounts

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
	store := NewStore(pool)
	id := testdb.Account(t, pool, uid)
	if _, err := pool.Exec(ctx,
		`UPDATE accounts SET description = 'D1', note = 'N1' WHERE id = $1`, id); err != nil {
		t.Fatalf("seed: %v", err)
	}

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"name":"Renamed wallet"}`
		}
		var req UpdateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := store.Update(ctx, uid, id, req, false)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
	}
}
