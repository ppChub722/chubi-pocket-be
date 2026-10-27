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

// 0.4.1-1 D2: connecting an existing transaction makes it the entry's transaction, connected and
// synced: the entry takes its amount and date, the transaction takes the kind category (its own is
// kept and comes back on a disconnect). An entry with a transaction can't connect another; a
// transaction made from the entry can't be disconnected.
func TestDebtLinkExistingTransaction(t *testing.T) {
	f := newDebtHFix(t)

	made := f.call("POST", entriesPath("n:Mom"), map[string]any{
		"direction": "paid", "amount": 350, "date": "2026-10-03", "description": "for mom",
	}, "", http.StatusCreated)
	entryID := obj(made, "entry")["id"].(string)
	entry := "/api/v1/personal-debts/" + entryID
	lunch := f.plainTx(transactions.TypeExpense, 500)

	linked := f.call("POST", entry+"/link", map[string]any{"transaction_id": lunch}, "linkDebtEntryTx", http.StatusOK)
	tx := obj(linked, "transaction")
	if tx["id"] != lunch || obj(linked, "entry", "transaction")["connected"] != true ||
		num(linked, "entry", "amount") != 500 || obj(linked, "entry")["date"] != "2026-10-04" ||
		obj(tx, "category")["system_kind"] != "LOAN_GIVEN" || tx["description"] != "for mom" ||
		obj(tx, "counterparty")["connected"] != true || num(linked, "person_balance", "net") != 500 {
		t.Fatalf("after link: %v", linked)
	}
	if f.balance() != 500 {
		t.Errorf("wallet = %v, want 500 (the connected tx keeps its wallet)", f.balance())
	}

	// One value: editing the transaction moves the entry; editing the entry moves the transaction.
	amt := 450.0
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, uuid.MustParse(lunch), transactions.UpdateRequest{Amount: &amt}); err != nil {
		t.Fatal(err)
	}
	if got := f.call("GET", entry, nil, "getDebtEntry", http.StatusOK); num(got, "entry", "amount") != 450 {
		t.Errorf("entry after the tx edit = %v", got["entry"])
	}
	edited := f.call("PUT", entry, map[string]any{"amount": 400}, "updateDebtEntry", http.StatusOK)
	if num(edited, "transaction", "amount") != 400 || f.balance() != 600 {
		t.Errorf("tx after the entry edit = %v (wallet %v)", edited["transaction"], f.balance())
	}
	f.code("PUT", entry, map[string]any{"account_id": nil}, http.StatusBadRequest, "VALIDATION_ERROR")

	f.code("POST", entry+"/link", map[string]any{"transaction_id": f.plainTx(transactions.TypeExpense, 9)},
		http.StatusConflict, "ENTRY_HAS_TRANSACTION")
	other := f.call("POST", entriesPath("n:Mom"), map[string]any{"direction": "paid", "amount": 20}, "", http.StatusCreated)
	otherPath := "/api/v1/personal-debts/" + obj(other, "entry")["id"].(string)
	f.code("POST", otherPath+"/link", map[string]any{"transaction_id": f.plainTx(transactions.TypeIncome, 50)},
		http.StatusBadRequest, "TX_NOT_LINKABLE")
	f.code("POST", otherPath+"/link", map[string]any{"transaction_id": uuid.New()}, http.StatusNotFound, "TRANSACTION_NOT_FOUND")
	f.code("POST", otherPath+"/link", map[string]any{"transaction_id": lunch}, http.StatusConflict, "DEBT_TX_ALREADY_LINKED")

	gone := f.call("POST", entry+"/unlink", nil, "unlinkDebtEntryTx", http.StatusOK)
	if gone["transaction"] != nil || obj(gone, "entry")["transaction"] != nil || num(gone, "entry", "amount") != 400 {
		t.Errorf("after unlink: %v", gone["entry"])
	}
	back, err := f.txs.Get(f.ctx, f.me, uuid.MustParse(lunch))
	if err != nil || back.Counterparty != nil || back.Category != nil || back.Amount != 400 {
		t.Errorf("the disconnected tx = %+v (%v), want plain, its old category (none), amount 400", back, err)
	}
	if ts, _ := obj(gone, "effects")["transactions"].([]any); len(ts) != 1 {
		t.Errorf("effects.transactions = %v", obj(gone, "effects")["transactions"])
	}

	// A transaction made from the entry can't be disconnected.
	own := f.call("POST", entriesPath("n:Mom"), map[string]any{"direction": "paid", "amount": 30, "account_id": f.wallet}, "", http.StatusCreated)
	f.code("POST", "/api/v1/personal-debts/"+obj(own, "entry")["id"].(string)+"/unlink", nil,
		http.StatusConflict, "TX_NOT_DISCONNECTABLE")
	f.code("POST", otherPath+"/unlink", nil, http.StatusConflict, "TX_NOT_DISCONNECTABLE")
}

// D10: create a debt / a repayment with link_transaction_id in one call; deleting the entry keeps a
// connected tx (back to its old category).
func TestCreateWithLinkedTransaction(t *testing.T) {
	f := newDebtHFix(t)
	transfer := f.plainTx(transactions.TypeExpense, 300)
	lent := f.call("POST", entriesPath("n:Lee"), map[string]any{
		"direction": "paid", "link_transaction_id": transfer,
	}, "createDebtEntry", http.StatusCreated)
	if num(lent, "entry", "amount") != 300 || obj(lent, "transaction")["id"] != transfer ||
		obj(lent, "transaction", "category")["system_kind"] != "LOAN_GIVEN" || num(lent, "person_balance", "net") != 300 {
		t.Fatalf("lend with a linked tx = %v", lent)
	}
	f.code("POST", entriesPath("n:Lee"), map[string]any{"direction": "paid", "amount": 5,
		"link_transaction_id": f.plainTx(transactions.TypeExpense, 5), "account_id": f.wallet}, http.StatusBadRequest, "VALIDATION_ERROR")
	f.code("POST", entriesPath("n:Lee"), map[string]any{"direction": "paid"}, http.StatusBadRequest, "VALIDATION_ERROR")

	back := f.plainTx(transactions.TypeIncome, 120)
	rep := f.call("POST", repayPath("n:lee"), map[string]any{"kind": "receive", "link_transaction_id": back, "mark_all_paid": true},
		"createDebtRepayment", http.StatusCreated)
	if obj(rep, "entry")["kind"] != "repayment" || obj(rep, "transaction", "category")["system_kind"] != "DEBT_RECEIVED" ||
		num(rep, "person_balance", "net") != 180 || num(rep, "person_balance", "open_count") != 0 {
		t.Fatalf("repayment with a linked tx = %v", rep)
	}

	del := f.call("DELETE", "/api/v1/personal-debts/"+obj(lent, "entry")["id"].(string), nil, "deleteDebtEntry", http.StatusOK)
	if del["transaction_id"] != nil || num(del, "person_balance", "net") != -120 {
		t.Errorf("delete = %v", del)
	}
	row, err := f.txs.Get(f.ctx, f.me, uuid.MustParse(transfer))
	if err != nil || row.Counterparty != nil || f.balance() != 1000-300-5+120 {
		t.Errorf("the connected tx after the delete = %+v (%v), wallet %v", row, err, f.balance())
	}
}
