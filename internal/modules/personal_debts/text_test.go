package personal_debts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	svc := NewService(NewStore(pool), txs)
	txs.WithDebtsCreator(svc.CreateForTransactionTx)
	txs.WithDebtLinks(svc)
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

	res, err := svc.CreateEntry(ctx, uid, "n:Aom", CreateEntryRequest{
		Direction: DirectionPaid, Amount: 300, AccountID: &wallet,
		Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testdb.CheckText(t, "create", res.Entry.Description, res.Entry.Note, "D1", "N1")
	testdb.CheckText(t, "create tx", res.Transaction.Description, res.Transaction.Note, "D1", "N1")

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"amount":350}`
		}
		var req UpdateEntryRequest
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		got, err := svc.UpdateEntry(ctx, uid, res.Entry.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Entry.Description, got.Entry.Note, st.WantD, st.WantN)
		testdb.CheckText(t, body+" (tx)", got.Transaction.Description, got.Transaction.Note, st.WantD, st.WantN)
	}

	if _, err := txs.Create(ctx, uid, transactions.CreateRequest{
		Type: transactions.TypeExpense, AccountID: &wallet, Amount: 200, Date: "2026-10-10",
		Description: testdb.Str("Dinner"),
		Splits:      []transactions.SplitInput{{PersonName: "Beam", OwedAmount: 100}},
	}); err != nil {
		t.Fatalf("create split bill: %v", err)
	}
	var desc *string
	if err := pool.QueryRow(ctx, `SELECT description FROM personal_debts
		WHERE user_id = $1 AND counterparty_person_name = 'Beam'`, uid).Scan(&desc); err != nil {
		t.Fatalf("read split entry: %v", err)
	}
	testdb.CheckText(t, "split entry", desc, nil, "Dinner", "")
}
