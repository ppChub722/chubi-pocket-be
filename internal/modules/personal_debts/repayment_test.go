package personal_debts

import (
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/ppChub722/chubi-pocket-be/internal/modules/transactions"
)

func repayPath(key string) string {
	return "/api/v1/personal-debts/people/" + url.PathEscape(key) + "/repayments"
}

func personPath(key, q string) string {
	return "/api/v1/personal-debts/people/" + url.PathEscape(key) + q
}

// TestRepaymentLeeScenario — the owner's example (decisions.md "Debt model"): debts −60, −500 (with a
// linked income tx that does NOT change the balance), −70; a 400 repayment I pay → −230; a final
// 230 clears it. The statement interleaves debts and repayment transactions.
func TestRepaymentLeeScenario(t *testing.T) {
	f := newDebtHFix(t)
	day := func(n int) string { return time.Now().AddDate(0, 0, n).Format("2006-01-02") }
	debt := func(amount float64, date string, wallet bool) map[string]any {
		body := map[string]any{"direction": "received", "amount": amount, "date": date}
		if wallet {
			body["account_id"] = f.wallet
		}
		return f.call("POST", entriesPath("n:Lee"), body, "createDebtEntry", http.StatusCreated)
	}

	debt(60, day(-10), false) // Lee paid for my food
	borrowed := debt(500, day(-9), true)
	if num(borrowed, "person_balance", "net") != -560 || f.balance() != 1500 {
		t.Fatalf("borrow 500: net %v wallet %v, want −560 / 1500 (the linked tx moves the wallet, not the net)",
			num(borrowed, "person_balance", "net"), f.balance())
	}

	pay := f.call("POST", repayPath("n:lee"), map[string]any{
		"kind": "pay", "amount": 400, "date": day(-8), "account_id": f.wallet,
		"description": "Pay Lee back", "tag_ids": []uuid.UUID{f.tag},
	}, "createDebtRepayment", http.StatusCreated)
	entry := obj(pay, "entry")
	tx := obj(pay, "transaction")
	if entry["kind"] != "repayment" || entry["direction"] != "paid" || entry["paid"] != false || entry["origin"] != nil {
		t.Fatalf("repayment entry = %v", entry)
	}
	if tx["type"] != "expense" || obj(tx, "category")["system_kind"] != "DEBT_PAID" {
		t.Fatalf("repayment tx = %v", tx)
	}
	cp := obj(tx, "counterparty")
	if cp["kind"] != "repayment" || cp["key"] != "n:lee" || cp["entry_id"] != entry["id"] || cp["connected"] != false {
		t.Errorf("counterparty = %v", cp)
	}
	if tags, _ := tx["tags"].([]any); len(tags) != 1 {
		t.Errorf("repayment tags = %v", tx["tags"])
	}
	item := obj(pay, "item")
	if item["kind"] != "repayment" || item["id"] != entry["id"] || num(item, "signed_amount") != 400 ||
		item["direction"] != "paid" || item["paid"] != nil || item["in_current_cycle"] != true {
		t.Errorf("item = %v", item)
	}
	if num(pay, "person_balance", "net") != -160 || f.balance() != 1100 {
		t.Errorf("after pay 400: net %v wallet %v, want −160 / 1100", num(pay, "person_balance", "net"), f.balance())
	}
	if bs, _ := pay["account_balances"].([]any); len(bs) != 1 {
		t.Errorf("account_balances = %v", pay["account_balances"])
	}
	eff := obj(pay, "effects")
	if ds, _ := eff["debts"].([]any); len(ds) != 1 {
		t.Errorf("effects.debts = %v", eff["debts"])
	}
	if ts, _ := eff["transactions"].([]any); len(ts) != 1 {
		t.Errorf("effects.transactions = %v", eff["transactions"])
	}
	if ps, _ := eff["person_balances"].([]any); len(ps) != 1 || num(ps[0].(map[string]any), "net") != -160 {
		t.Errorf("effects.person_balances = %v", eff["person_balances"])
	}

	last := debt(70, day(-7), false)
	if num(last, "person_balance", "net") != -230 {
		t.Fatalf("net = %v, want −230 (60 + 500 + 70 − 400)", num(last, "person_balance", "net"))
	}
	page := f.call("GET", personPath("n:lee", ""), nil, "getDebtPerson", http.StatusOK)
	if num(page, "net") != -230 || num(page, "counts", "entries") != 4 || num(page, "counts", "money") != 2 ||
		num(page, "entry_count") != 4 || num(page, "open_count") != 3 || page["cleared_at"] != nil {
		t.Errorf("person page = net %v counts %v entry_count %v open %v cleared %v",
			page["net"], page["counts"], page["entry_count"], page["open_count"], page["cleared_at"])
	}

	// No wallet, no create_transaction: a ledger-only repayment ("paid outside the app", D3).
	final := f.call("POST", repayPath("n:lee"), map[string]any{"kind": "pay", "amount": 230, "date": day(-6)},
		"createDebtRepayment", http.StatusCreated)
	finalID := uuid.MustParse(obj(final, "entry")["id"].(string))
	if num(final, "person_balance", "net") != 0 || obj(final, "person_balance")["cleared_at"] != day(-6) ||
		final["transaction"] != nil || f.balance() != 1100 {
		t.Errorf("after the final 230: %v tx %v (wallet %v)", final["person_balance"], final["transaction"], f.balance())
	}

	all := f.call("GET", personPath("n:lee", "?cycle=all"), nil, "getDebtPerson", http.StatusOK)
	items := all["items"].([]any)
	wantKinds := []string{"repayment", "debt", "repayment", "debt", "debt"} // newest first
	wantSigned := []float64{230, -70, 400, -500, -60}
	if len(items) != 5 {
		t.Fatalf("statement = %d items, want 5: %v", len(items), items)
	}
	for i, it := range items {
		m := it.(map[string]any)
		if m["kind"] != wantKinds[i] || m["signed_amount"] != wantSigned[i] {
			t.Errorf("item %d = %v %v, want %s %v", i, m["kind"], m["signed_amount"], wantKinds[i], wantSigned[i])
		}
		if m["in_current_cycle"] != false {
			t.Errorf("item %d in_current_cycle = %v, want false (the cycle ended with the 230)", i, m["in_current_cycle"])
		}
	}
	if d := items[3].(map[string]any); obj(d, "transaction")["id"] == nil || d["paid"] != false {
		t.Errorf("the borrowed debt keeps its tx + paid label: %v", d)
	}
	if r := items[0].(map[string]any); r["transaction"] != nil || r["id"] != finalID.String() {
		t.Errorf("the ledger-only repayment line = %v", r)
	}
	if cur := f.call("GET", personPath("n:lee", ""), nil, "getDebtPerson", http.StatusOK); len(cur["items"].([]any)) != 0 ||
		num(cur, "counts", "earlier") != 5 {
		t.Errorf("current cycle after clearing = %v / counts %v", cur["items"], cur["counts"])
	}
	people := f.call("GET", "/api/v1/personal-debts/people", nil, "listDebtPeople", http.StatusOK)
	if rows := people["data"].([]any); len(rows) != 1 || num(rows[0].(map[string]any), "net") != 0 {
		t.Errorf("people (Lee still listed: unpaid debts) = %v", rows)
	}
	reps := f.call("GET", entriesPath("n:lee")+"?kind=repayment", nil, "listDebtEntries", http.StatusOK)
	if rows := reps["data"].([]any); len(rows) != 2 {
		t.Errorf("entries?kind=repayment = %v", rows)
	}

	// Editing the repayment's tx moves the entry with it (one value, D1); the link stays.
	amt := 300.0
	w, err := f.txs.UpdateWrite(f.ctx, f.me, uuid.MustParse(tx["id"].(string)), transactions.UpdateRequest{Amount: &amt})
	if err != nil {
		t.Fatalf("edit the repayment tx: %v", err)
	}
	if pb, ok := w.PersonBalance.(DebtPerson); !ok || pb.Net != -100 {
		t.Errorf("after 400 → 300: person_balance %+v, want −100", w.PersonBalance)
	}
	if w.Rows[0].Counterparty == nil || w.Rows[0].Counterparty.Kind != KindRepayment || f.balance() != 1200 {
		t.Errorf("edited row keeps the link: %+v (wallet %v)", w.Rows[0].Counterparty, f.balance())
	}
	cat := uuid.New()
	if _, err := f.txs.UpdateWrite(f.ctx, f.me, w.Rows[0].ID, transactions.UpdateRequest{CategoryID: &cat}); err == nil {
		t.Errorf("a repayment's category must stay")
	}

	// Deleting the ledger-only repayment row moves the balance back.
	dr, err := f.svc.DeleteEntry(f.ctx, f.me, finalID)
	if err != nil {
		t.Fatalf("delete the repayment: %v", err)
	}
	if dr.PersonBalance.Net != -330 || dr.PersonBalance.ClearedAt != nil || dr.TransactionID != nil {
		t.Errorf("after deleting the 230: %+v, want −330, never cleared, no tx", dr)
	}

	// The person's money list: the borrowed debt's tx + the repayment's.
	res, err := f.txs.List(f.ctx, f.me, transactions.ListFilter{CounterpartyKey: "n:lee", Page: 1, PerPage: 50})
	if err != nil || len(res.Data) != 2 {
		t.Fatalf("counterparty_key=n:lee = %d rows (%v), want 2", len(res.Data), err)
	}

	// Deleting the repayment's tx keeps the repayment (D1): balance unchanged, wallet back.
	del, err := f.txs.DeleteWrite(f.ctx, f.me, w.Rows[0].ID, false)
	if err != nil {
		t.Fatal(err)
	}
	if pb, ok := del.PersonBalance.(DebtPerson); !ok || pb.Net != -330 || f.balance() != 1500 {
		t.Errorf("after deleting the repayment tx: %+v (wallet %v), want −330 / 1500", del.PersonBalance, f.balance())
	}
	got, err := f.svc.GetEntry(f.ctx, f.me, uuid.MustParse(entry["id"].(string)))
	if err != nil || got.Transaction != nil || got.Entry.Amount != 300 {
		t.Errorf("the repayment after its tx went = %+v (%v), want ledger-only 300", got, err)
	}
}

// TestRepaymentReceiveAndKeys — "they owe me" person: a receive repayment is an income (−); a linked
// contact and a typed name are separate keys; mark_paid_ids only labels.
func TestRepaymentReceiveAndKeys(t *testing.T) {
	f := newDebtHFix(t)
	key := "c:" + f.contact.String()
	lent := f.call("POST", entriesPath(key), map[string]any{"direction": "paid", "amount": 100, "date": "2026-10-01"},
		"createDebtEntry", http.StatusCreated)
	lentID := obj(lent, "entry")["id"].(string)
	f.call("POST", entriesPath("n:Bee"), map[string]any{"direction": "paid", "amount": 40, "date": "2026-10-01"},
		"createDebtEntry", http.StatusCreated)

	got := f.call("POST", repayPath(key), map[string]any{
		"kind": "receive", "amount": 30, "date": "2026-10-05", "account_id": f.wallet, "mark_paid_ids": []string{lentID},
	}, "createDebtRepayment", http.StatusCreated)
	tx := obj(got, "transaction")
	cp := obj(tx, "counterparty")
	if tx["type"] != "income" || obj(tx, "category")["system_kind"] != "DEBT_RECEIVED" ||
		cp["kind"] != "repayment" || cp["key"] != key || obj(cp, "person")["name"] != "Bee Live" || obj(cp, "person")["linked"] != true {
		t.Fatalf("receive tx = %v", tx)
	}
	f.code("POST", repayPath(key), map[string]any{"kind": "pay", "amount": 5, "tag_ids": []uuid.UUID{f.tag}},
		http.StatusBadRequest, "LEDGER_ONLY_NO_WALLET")
	if num(got, "person_balance", "net") != 70 || num(got, "person_balance", "open_count") != 0 ||
		num(got, "item", "signed_amount") != -30 || obj(got, "item")["direction"] != "received" || f.balance() != 1030 {
		t.Errorf("after receive 30: %v item %v (wallet %v)", got["person_balance"], got["item"], f.balance())
	}
	if e, _ := f.svc.GetEntry(f.ctx, f.me, uuid.MustParse(lentID)); !e.Entry.Paid || e.Entry.Amount != 100 {
		t.Errorf("the marked debt = %+v, want paid label + amount 100", e.Entry)
	}
	typed := f.call("GET", personPath("n:bee", ""), nil, "getDebtPerson", http.StatusOK)
	if num(typed, "net") != 40 || len(typed["items"].([]any)) != 1 {
		t.Errorf("typed-name Bee is its own person: %v", typed)
	}

	f.code("POST", repayPath(key), map[string]any{"kind": "give", "amount": 5}, http.StatusBadRequest, "VALIDATION_ERROR")
	f.code("POST", repayPath(key), map[string]any{"kind": "pay", "amount": 0}, http.StatusBadRequest, "VALIDATION_ERROR")
	f.code("POST", repayPath("c:"+uuid.NewString()), map[string]any{"kind": "pay", "amount": 5}, http.StatusBadRequest, "CONTACT_NOT_FOUND")
	f.code("POST", repayPath(key), map[string]any{"kind": "pay", "amount": 5, "mark_paid_ids": []uuid.UUID{uuid.New()}},
		http.StatusBadRequest, "ENTRY_NOT_FOUND")

	// Deleting the contact drops the repayment back to the typed name (level 1), amounts unchanged.
	tr, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.svc.RestoreOnContactDeleteTx(f.ctx, tr, f.me, f.contact); err != nil {
		t.Fatal(err)
	}
	if _, err := tr.Exec(f.ctx, `DELETE FROM contacts WHERE id = $1`, f.contact); err != nil {
		t.Fatal(err)
	}
	if err := tr.Commit(f.ctx); err != nil {
		t.Fatal(err)
	}
	merged := f.call("GET", personPath("n:bee live", ""), nil, "getDebtPerson", http.StatusOK)
	if num(merged, "net") != 70 || num(merged, "entry_count") != 2 {
		t.Errorf("after contact delete: %v", merged)
	}
	names, err := f.svc.UnlinkedNames(f.ctx, f.me)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, n := range names {
		counts[n.Name] = n.Count
	}
	if counts["Bee Live"] != 2 || counts["Bee"] != 1 {
		t.Errorf("unlinked names = %v, want Bee Live ×2 (debt + repayment), Bee ×1", counts)
	}
}

// TestRepayEntryMakesRepaymentTx — the pending settle_debt path writes a repayment row in the
// opposite direction of the debt, with its transaction when a wallet is given (0.4.1-1 D3).
func TestRepayEntryMakesRepaymentTx(t *testing.T) {
	f := newDebtHFix(t)
	owe, err := f.svc.CreateEntry(f.ctx, f.me, "n:Mom", CreateEntryRequest{Direction: DirectionReceived, Amount: 90})
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.svc.RepayEntry(f.ctx, f.me, owe.Entry.ID, 50, &f.wallet, nil, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Transaction == nil || res.Transaction.Type != transactions.TypeExpense || res.Transaction.Counterparty == nil ||
		res.Transaction.Counterparty.Person.Name != "Mom" || res.Entry.Kind != KindRepayment ||
		res.PersonBalance.Net != -40 || f.balance() != 950 {
		t.Fatalf("repay = %+v / %+v (wallet %v)", res.Transaction, res.PersonBalance, f.balance())
	}
	var rows int
	if err := f.pool.QueryRow(f.ctx, `SELECT COUNT(*) FROM personal_debts WHERE user_id = $1`, f.me).Scan(&rows); err != nil || rows != 2 {
		t.Errorf("personal_debts rows = %d (%v), want 2 (the debt + the repayment row)", rows, err)
	}
}
