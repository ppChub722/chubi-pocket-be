package projects

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

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
	testdb.CheckText(t, "create project", p.Event.Description, p.Event.Note, "D1", "N1")
	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"planned_amount":9000}`
		}
		var req UpdateProjectRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := svc.Update(ctx, uid, p.Event.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, "project "+body, got.Event.Description, got.Event.Note, st.WantD, st.WantN)
	}

	m, err := store.MemberByUserID(ctx, p.Event.ID, uid)
	if err != nil {
		t.Fatalf("owner member: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	pt, err := store.InsertPTTx(ctx, tx, p.Event.ID, uid, CreateProjectTransactionRequest{
		TransactionMemberID: m.ID, Type: "expense", Amount: 100, Date: "2026-10-10",
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
			body = `{"amount":120}`
		}
		var req UpdateProjectTransactionRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		got, err := store.UpdatePTTx(ctx, tx, p.Event.ID, pt.ID, uid, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit: %v", err)
		}
		testdb.CheckText(t, "row "+body, got.Description, got.Note, st.WantD, st.WantN)
	}
}
