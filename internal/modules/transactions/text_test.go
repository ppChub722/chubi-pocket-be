package transactions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — see tags/text_test.go. A transfer's two rows
// both follow; search matches the description too.
func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	store := NewStore(pool)
	cats := categories.NewService(categories.NewStore(pool))
	svc := NewService(store, cats)
	seed, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	if err := cats.SeedForUser(ctx, seed, uid); err != nil {
		t.Fatalf("seed categories: %v", err)
	}
	if err := seed.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	wallet := testdb.Account(t, pool, uid)
	other := testdb.Account(t, pool, uid)
	cat := testdb.Category(t, pool, uid, "Food")

	created, err := svc.Create(ctx, uid, CreateRequest{
		Type: TypeExpense, AccountID: &wallet, CategoryID: &cat, Amount: 60, Date: "2026-10-10",
		Description: testdb.Str(" D1 "), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	tx := created.(*TransactionDetail)
	testdb.CheckText(t, "create", tx.Description, tx.Note, "D1", "N1")

	transfer, err := svc.Create(ctx, uid, CreateRequest{
		Type: TypeTransfer, AccountID: &wallet, TransferToAccountID: &other, Amount: 10,
		Date: "2026-10-10", Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create transfer: %v", err)
	}
	rows := transfer.(*TransferResponse).Rows

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"amount":70}`
		}
		for _, id := range []uuid.UUID{tx.ID, rows[0].ID} {
			var req UpdateRequest
			if err := json.Unmarshal([]byte(body), &req); err != nil {
				t.Fatalf("%s: %v", body, err)
			}
			if _, err := svc.Update(ctx, uid, id, req); err != nil {
				t.Fatalf("%s: %v", body, err)
			}
		}
		for _, id := range []uuid.UUID{tx.ID, rows[0].ID, rows[1].ID} {
			got, err := store.GetByID(ctx, uid, id)
			if err != nil {
				t.Fatalf("get: %v", err)
			}
			testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
		}
	}

	found, _, err := store.List(ctx, uid, ListFilter{Q: "d2", Page: 1, PerPage: 10})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(found) != 3 {
		t.Errorf("search by description: %d rows, want 3", len(found))
	}

	// SetText (a project row's copy) writes both, nil = clear.
	req := UpdateRequest{}
	req.SetText(testdb.Str("D3"), nil)
	if _, err := svc.Update(ctx, uid, tx.ID, req); err != nil {
		t.Fatalf("set text: %v", err)
	}
	got, _ := store.GetByID(ctx, uid, tx.ID)
	testdb.CheckText(t, "SetText", got.Description, got.Note, "D3", "")
}
