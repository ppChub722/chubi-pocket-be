package personal_debts

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/categories"
	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
	"github.com/ppChub722/chubi-pocket-be/internal/platform/testdb"
)

// TestUpdateTextFields — see tags/text_test.go (null detection lives in
// the handler, so the test sets the Clear* flags the same way). Splits
// give the debt the bill's description; settling copies it to the
// settlement transaction.
func TestUpdateTextFields(t *testing.T) {
	pool := testdb.Pool(t)
	ctx := context.Background()
	uid := testdb.User(t, pool)
	cats := categories.NewService(categories.NewStore(pool))
	txs := transactions.NewService(transactions.NewStore(pool), cats)
	store := NewStore(pool)
	svc := NewService(store, txs)
	txs.WithDebtsCreator(svc.CreateForTransactionTx)
	txs.WithDebtValidator(svc.ValidateOwnership)
	txs.WithDebtAutoBumper(svc.AutoBumpInTx)
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

	d, err := store.Create(ctx, uid, CreateRequest{
		Direction: DirectionOwedToMe, CounterpartyPersonName: "Aom", Amount: 300, Currency: "THB",
		Description: testdb.Str("D1"), Note: testdb.Str("N1"),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	testdb.CheckText(t, "create", d.Description, d.Note, "D1", "N1")

	for i, st := range testdb.Steps {
		body := st.Body
		if i == 0 {
			body = `{"amount":350}`
		}
		var req UpdateRequest
		var raw map[string]json.RawMessage
		if err := json.Unmarshal([]byte(body), &req); err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		_ = json.Unmarshal([]byte(body), &raw)
		req.ClearDescription = string(raw["description"]) == "null"
		req.ClearNote = string(raw["note"]) == "null"
		got, err := store.Update(ctx, uid, d.ID, req)
		if err != nil {
			t.Fatalf("%s: %v", body, err)
		}
		testdb.CheckText(t, body, got.Description, got.Note, st.WantD, st.WantN)
	}

	res, err := svc.Settle(ctx, uid, d.ID, SettleRequest{Amount: testdb.Float(50)})
	if err != nil {
		t.Fatalf("settle: %v", err)
	}
	stx := res.Transaction.(*transactions.TransactionDetail)
	testdb.CheckText(t, "settlement tx", stx.Description, stx.Note, "D2", "")

	wallet := testdb.Account(t, pool, uid)
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
		t.Fatalf("read split debt: %v", err)
	}
	testdb.CheckText(t, "split debt", desc, nil, "Dinner", "")
}
