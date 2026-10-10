package scheduled_transactions

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — see tags/text_test.go. Pay-now then files the
// payment with description = the schedule's name, note = its note.
func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	svc := NewService(NewStore(pool))

	s, err := svc.Create(ctx, uid, CreateRequest{
		Name: "Netflix", Type: "expense", EntryType: "recurring", Amount: 419,
		AccountID: testdb.Account(t, pool, uid), CategoryID: testdb.Category(t, pool, uid, "Subscriptions"),
		BillingCycle: "monthly", NextBillingDate: "2026-10-15",
		Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testdb.CheckText(t, "create", s.Description, s.Note, "D1", "N1")

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"amount":499}`
		}
		var req UpdateRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := svc.Update(ctx, uid, s.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
	}

	gen, err := svc.GenerateNow(ctx, uid, s.ID)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	var d, n *string
	if err := pool.QueryRow(ctx, `SELECT description, note FROM transactions WHERE id = $1`,
		gen.GeneratedTransaction.ID).Scan(&d, &n); err != nil {
		t.Fatalf("read tx: %v", err)
	}
	testdb.CheckText(t, "generated tx", d, n, "Netflix", "N2")
}
