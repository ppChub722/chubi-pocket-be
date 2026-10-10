package projects

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — see tags/text_test.go, for a project and one of
// its rows.
func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	store := NewStore(pool)
	svc := NewService(store)

	p, err := svc.Create(ctx, uid, CreateProjectRequest{
		Name: "Trip", Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testdb.CheckText(t, "create project", p.Description, p.Note, "D1", "N1")
	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"planned_amount":9000}`
		}
		var req UpdateProjectRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := store.Update(ctx, uid, p.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, "project "+body, got.Description, got.Note, st.WantD, st.WantN)
	}

	m, err := store.MemberByUserID(ctx, p.ID, uid)
	if err != nil {
		t.Fatalf("owner member: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	pt, err := store.InsertPTTx(ctx, tx, p.ID, uid, CreateProjectTransactionRequest{
		TransactionMemberID: m.ID, Type: "expense", Amount: 100, Currency: "THB", Date: "2026-10-10",
		Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("insert row: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}
	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"amount":120,"tags":["food"]}`
		}
		var req UpdateProjectTransactionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		got, err := store.UpdatePTTx(ctx, tx, p.ID, pt.ID, uid, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		testdb.CheckText(t, "row "+body, got.Description, got.Note, st.WantD, st.WantN)
	}
}

// TestSuggestedUpdateCarriesText — the "update to match" suggestion for a
// personal copy maps description → description and note → note.
func TestSuggestedUpdateCarriesText(t *testing.T) {
	after := &ProjectTransaction{Date: "2026-10-10", Description: testdb.Str("Dinner"), Note: nil}
	sug := suggestedUpdate(100, 100, 50, 120, 60, after)
	testdb.CheckText(t, "suggested", sug.Description, sug.Note, "Dinner", "")
	if sug.Amount != 120 {
		t.Errorf("amount = %v, want 120", sug.Amount)
	}
}
