package personal_debts

import (
	"net/http"
	"testing"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

func (f *debtHFix) plainTx(typ transactions.TxType, amount float64) string {
	f.t.Helper()
	res, err := f.txs.Create(f.ctx, f.me, transactions.CreateRequest{
		Type: typ, AccountID: &f.wallet, Amount: amount, Date: "2026-10-04",
	})
	if err != nil {
		f.t.Fatal(err)
	}
	return res.(*transactions.TransactionDetail).ID.String()
}

// A manual entry links one of my plain transactions of its direction: the link replaces the
// entry's own transaction, unlinking keeps the transaction, and the own one replaces a link.
func TestDebtLinkExistingTransaction(t *testing.T) {
	f := newDebtHFix(t)

	made := f.call("POST", entriesPath("n:Mom"), map[string]any{
		"direction": "paid", "amount": 350, "date": "2026-10-04", "account_id": f.wallet,
	}, "", http.StatusCreated)
	entry := "/api/v1/personal-debts/" + obj(made, "entry")["id"].(string)
	lunch := f.plainTx(transactions.TypeExpense, 500)
	if f.balance() != 150 {
		t.Fatalf("wallet = %v, want 150", f.balance())
	}

	linked := f.call("POST", entry+"/link", map[string]any{"transaction_id": lunch}, "linkDebtEntryTx", http.StatusOK)
	if linked["transaction"] != nil || obj(linked, "entry", "linked_transaction")["id"] != lunch ||
		obj(linked, "linked_transaction")["id"] != lunch {
		t.Fatalf("after link: %v", linked)
	}
	if f.balance() != 500 {
		t.Errorf("wallet = %v, want 500 (the own transaction went)", f.balance())
	}
	got := f.call("GET", entry, nil, "getDebtEntry", http.StatusOK)
	if obj(got, "linked_transaction")["id"] != lunch || num(got, "entry", "amount") != 350 {
		t.Errorf("detail = %v", got)
	}

	f.code("POST", entry+"/link", map[string]any{"transaction_id": f.plainTx(transactions.TypeIncome, 50)},
		http.StatusBadRequest, "TX_NOT_LINKABLE")
	f.code("POST", entry+"/link", map[string]any{"transaction_id": uuid.New()}, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	other := f.call("POST", entriesPath("n:Mom"), map[string]any{"direction": "paid", "amount": 20}, "", http.StatusCreated)
	f.code("POST", "/api/v1/personal-debts/"+obj(other, "entry")["id"].(string)+"/link",
		map[string]any{"transaction_id": lunch}, http.StatusConflict, "DEBT_TX_ALREADY_LINKED")

	gone := f.call("POST", entry+"/unlink", nil, "unlinkDebtEntryTx", http.StatusOK)
	// 500 + the income of 50 above (same wallet).
	if obj(gone, "entry")["linked_transaction"] != nil || f.balance() != 550 {
		t.Errorf("after unlink: %v, wallet %v", gone["entry"], f.balance())
	}
	var n int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM transactions WHERE id = $1`, lunch).Scan(&n); err != nil || n != 1 {
		t.Errorf("the unlinked transaction is gone (%d, %v)", n, err)
	}

	f.call("POST", entry+"/link", map[string]any{"transaction_id": lunch}, "", http.StatusOK)
	own := f.call("PUT", entry, map[string]any{"create_transaction": true, "account_id": f.wallet}, "", http.StatusOK)
	if obj(own, "transaction") == nil || obj(own, "entry")["linked_transaction"] != nil {
		t.Errorf("own transaction over a link: %v", own)
	}
}
