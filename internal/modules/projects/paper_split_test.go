package projects

import (
	"testing"
)

// One split bill from start to end: A pays 300 for A, Bee (an app user) and Dee (a name). Bee is
// invited, joins, claims as a debt; A edits, Bee's books wait for "อัปเดต"; A's debts and Bee's
// debt are partners.
func TestSplitBillLife(t *testing.T) {
	f := newFx(t)
	f.settings(f.b, map[string]any{"auto_accept_debts": false, "auto_sync_debts": false})
	A, B := f.as(f.a), f.as(f.b)

	created := A.do("POST", "/split-bills", map[string]any{
		"paper": map[string]any{"type": "expense", "amount": 300, "date": "2026-11-01", "description": "Dinner",
			"split_mode": "equal", "people": []any{
				map[string]any{"me": true, "paid": 300},
				map[string]any{"contact_id": f.contactB},
				map[string]any{"person_name": "Dee"},
			}},
		"my_books": map[string]any{"pay": "debt", "account_id": f.walletA, "category_id": f.catA},
	}, "createSplitBill", 201)
	paper := obj(created, "paper")
	id := str(paper, "id")
	if !near(num(person(paper, "Bee"), "share"), 100) || str(person(paper, "Bee"), "status") != "invited" {
		t.Fatalf("Bee on the paper: %v", person(paper, "Bee"))
	}
	if !obj(paper, "my_books")["claimed"].(bool) {
		t.Errorf("A's books not claimed: %v", obj(paper, "my_books"))
	}
	if b := f.balance(f.walletA); !near(b, 700) {
		t.Errorf("A's wallet %v, want 700 (A paid the whole bill)", b)
	}
	if v := f.personNet(f.a, "Bee"); !near(v, 100) {
		t.Errorf("A's book: Bee owes %v, want 100", v)
	}
	if v := f.personNet(f.a, "Dee"); !near(v, 100) {
		t.Errorf("A's book: Dee owes %v, want 100", v)
	}
	if f.notices(f.b, "bill_invite") != 1 {
		t.Errorf("Bee got no bill_invite")
	}

	// Bee reads the invite, joins, claims (debt branch).
	view := B.do("GET", "/split-bills/"+id, nil, "getSplitBill", 200)
	if !obj(view, "rights")["join"].(bool) {
		t.Errorf("invite: rights.join false")
	}
	B.fail("POST", "/split-bills/"+id+"/claim", nil, 400, "NOT_IN_BILL") // not joined yet
	B.do("POST", "/split-bills/"+id+"/join", nil, "joinSplitBill", 200)
	claimed := B.do("POST", "/split-bills/"+id+"/claim", map[string]any{"pay": "debt", "category_id": f.catB},
		"claimSplitBill", 200)
	bTx := obj(claimed, "paper", "my_books", "transaction")
	if !near(num(bTx, "amount"), 100) || bTx["account"] != nil {
		t.Errorf("Bee's tx = %v / %v, want 100 with no wallet", bTx["amount"], bTx["account"])
	}
	if v := f.personNet(f.b, "Alice"); !near(v, -100) {
		t.Errorf("Bee's book: owes Alice %v, want 100", -v)
	}
	if f.count(`SELECT COUNT(*) FROM personal_debts WHERE user_id = $1 AND counterpart_debt_id IS NOT NULL`, f.b) != 1 {
		t.Errorf("Bee's debt has no partner")
	}
	B.fail("POST", "/split-bills/"+id+"/claim", nil, 409, "ALREADY_CLAIMED")

	// A edits: 360, equal. A's own books follow; Bee's wait (auto sync off).
	ver := num(paper, "version")
	people := []any{}
	for _, p := range arr(B.do("GET", "/split-bills/"+id, nil, "", 200), "people") {
		people = append(people, map[string]any{"person_id": obj(p)["id"], "paid": map[bool]float64{true: 360}[str(obj(p), "name") == "Alice"]})
	}
	A.fail("PUT", "/split-bills/"+id, map[string]any{"version": ver + 5, "paper": map[string]any{
		"type": "expense", "amount": 360, "date": "2026-11-01", "description": "Dinner", "split_mode": "equal", "people": people}},
		409, "BILL_CHANGED")
	A.do("PUT", "/split-bills/"+id, map[string]any{"version": ver, "paper": map[string]any{
		"type": "expense", "amount": 360, "date": "2026-11-01", "description": "Dinner", "split_mode": "equal", "people": people}},
		"updateSplitBill", 200)
	if v := f.personNet(f.a, "Bee"); !near(v, 120) {
		t.Errorf("A's book after the edit: Bee owes %v, want 120", v)
	}
	if b := f.balance(f.walletA); !near(b, 640) {
		t.Errorf("A's wallet %v, want 640", b)
	}
	if v := f.personNet(f.b, "Alice"); !near(v, -100) {
		t.Errorf("Bee's book moved without asking: %v", v)
	}
	if f.notices(f.b, "bill_changed") != 1 {
		t.Errorf("Bee got no bill_changed")
	}
	bView := B.do("GET", "/split-bills/"+id, nil, "getSplitBill", 200)
	if obj(bView, "my_books")["in_sync"].(bool) {
		t.Errorf("Bee's books say in sync after A's edit")
	}
	// "อัปเดต" from Bee's transaction: tx and debt follow.
	B.do("POST", "/transactions/"+str(bTx, "id")+"/paper-sync", nil, "syncTransactionToPaper", 200)
	if v := f.personNet(f.b, "Alice"); !near(v, -120) {
		t.Errorf("Bee's book after อัปเดต: owes %v, want 120", -v)
	}
	if amt := f.num(`SELECT amount::float8 FROM transactions WHERE id = $1`, str(bTx, "id")); !near(amt, 120) {
		t.Errorf("Bee's tx %v, want 120", amt)
	}

	// Lists.
	if l := arr(B.do("GET", "/split-bills", nil, "listSplitBills", 200), "data"); len(l) != 1 {
		t.Errorf("Bee's split bills: %d, want 1", len(l))
	}

	// Bee unlinks the tx: it stays, plain; the paper still has Bee; a claim brings a new one.
	B.do("POST", "/transactions/"+str(bTx, "id")+"/unlink-paper", nil, "unlinkTransactionFromPaper", 200)
	if f.count(`SELECT COUNT(*) FROM transactions WHERE id = $1 AND split_transaction_id IS NULL`, str(bTx, "id")) != 1 {
		t.Errorf("unlinked tx still on the paper")
	}
	again := B.do("POST", "/split-bills/"+id+"/claim", map[string]any{"pay": "debt"}, "claimSplitBill", 200)
	if obj(again, "paper", "my_books", "transaction") == nil {
		t.Errorf("claim after unlink made no transaction")
	}
}

// Bee claims "paid from my wallet": no debt in Bee's book, and A (who holds "Bee owes me") hears it;
// A's auto_accept_repayments writes the repayment.
func TestSplitBillWalletClaimTellsPayer(t *testing.T) {
	f := newFx(t)
	f.settings(f.b, map[string]any{"auto_accept_debts": true, "auto_sync_debts": true})
	f.settings(f.a, map[string]any{"auto_accept_repayments": true, "default_account_id": f.walletA})
	// Bee auto-joins but claims by hand: turn the auto claim off by joining later.
	f.settings(f.b, map[string]any{"auto_accept_debts": false})
	A, B := f.as(f.a), f.as(f.b)
	id := str(obj(A.do("POST", "/split-bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 200, "date": "2026-11-01", "people": []any{
			map[string]any{"me": true, "paid": 200, "share": 100},
			map[string]any{"contact_id": f.contactB, "share": 100},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201), "paper"), "id")
	B.do("POST", "/split-bills/"+id+"/join", nil, "", 200)
	B.do("POST", "/split-bills/"+id+"/claim", map[string]any{"pay": "wallet", "account_id": f.walletB}, "claimSplitBill", 200)

	if b := f.balance(f.walletB); !near(b, 900) {
		t.Errorf("Bee's wallet %v, want 900", b)
	}
	if v := f.personNet(f.b, "Alice"); !near(v, 0) {
		t.Errorf("Bee's book has %v with Alice, want nothing", v)
	}
	if v := f.personNet(f.a, "Bee"); !near(v, 0) {
		t.Errorf("A's book: Bee still owes %v, want 0 (repayment auto-accepted)", v)
	}
	if b := f.balance(f.walletA); !near(b, 900) {
		t.Errorf("A's main wallet %v, want 900 (800 + Bee's 100)", b)
	}
	if f.count(`SELECT COUNT(*) FROM notifications WHERE recipient_user_id = $1 AND type = 'debt_repaid'
		AND payload->>'repayment_id' IS NOT NULL`, f.a) != 1 {
		t.Errorf("A got no applied debt_repaid")
	}
}

// A owner removes Bee (equal mode re-splits); Chai's books follow; A's part with Bee goes, Bee's
// books are kept (on_split_removed keep). Then the owner leaves (Chai owns it), and the last user
// leaving deletes the paper.
func TestSplitBillRemoveAndLeave(t *testing.T) {
	f := newFx(t)
	for _, u := range []any{f.b, f.c} {
		f.settings(f.b, nil)
		_ = u
	}
	f.settings(f.b, map[string]any{"auto_accept_debts": true, "auto_sync_debts": true, "on_split_removed": "keep"})
	f.settings(f.c, map[string]any{"auto_accept_debts": true, "auto_sync_debts": true})
	A, C := f.as(f.a), f.as(f.c)
	created := A.do("POST", "/split-bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 300, "date": "2026-11-01", "split_mode": "equal", "people": []any{
			map[string]any{"me": true, "paid": 300},
			map[string]any{"contact_id": f.contactB},
			map[string]any{"contact_id": f.contactC},
		}}, "my_books": map[string]any{"account_id": f.walletA}}, "", 201)
	paper := obj(created, "paper")
	id := str(paper, "id")
	if v := f.personNet(f.c, "Alice"); !near(v, -100) {
		t.Fatalf("Chai auto claim: owes %v, want 100", -v)
	}
	C.fail("DELETE", "/split-bills/"+id+"/people/"+str(person(paper, "Bee"), "id"), nil, 403, "NOT_BILL_OWNER")
	A.do("DELETE", "/split-bills/"+id+"/people/"+str(person(paper, "Bee"), "id"), nil, "removeSplitBillPerson", 200)

	if v := f.personNet(f.c, "Alice"); !near(v, -150) {
		t.Errorf("Chai after Bee left: owes %v, want 150", -v)
	}
	if v := f.personNet(f.a, "Bee"); !near(v, 0) {
		t.Errorf("A's book still has Bee owing %v", v)
	}
	if f.count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1 AND split_transaction_id IS NULL`, f.b) != 1 {
		t.Errorf("Bee's tx not kept as a plain one")
	}
	if f.notices(f.b, "bill_removed") != 1 {
		t.Errorf("Bee got no bill_removed")
	}

	left := A.do("POST", "/split-bills/"+id+"/leave", map[string]any{"books": "keep"}, "leaveSplitBill", 200)
	if left["deleted"].(bool) {
		t.Fatalf("paper deleted while Chai is on it")
	}
	if f.count(`SELECT COUNT(*) FROM project_transactions WHERE id = $1 AND owner_user_id = $2`, id, f.c) != 1 {
		t.Errorf("Chai is not the owner")
	}
	if f.notices(f.c, "bill_owner_changed") != 1 {
		t.Errorf("Chai got no bill_owner_changed")
	}
	if A.do("GET", "/split-bills", nil, "", 200) == nil {
		t.Fatal("list")
	}
	last := C.do("POST", "/split-bills/"+id+"/leave", map[string]any{"books": "delete"}, "leaveSplitBill", 200)
	if !last["deleted"].(bool) {
		t.Errorf("the last leaver did not delete the paper")
	}
	if f.count(`SELECT COUNT(*) FROM project_transactions WHERE id = $1`, id) != 0 {
		t.Errorf("paper still there")
	}
	if f.count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1`, f.c) != 0 {
		t.Errorf("Chai's books not deleted")
	}
}

// Declining keeps the person as a name; "คืนแล้ว" is a mark only.
func TestSplitBillDeclineAndMark(t *testing.T) {
	f := newFx(t)
	f.settings(f.b, map[string]any{"auto_accept_debts": false})
	A, B := f.as(f.a), f.as(f.b)
	paper := obj(A.do("POST", "/split-bills", map[string]any{"paper": map[string]any{
		"type": "expense", "amount": 100, "date": "2026-11-01", "split_mode": "equal", "people": []any{
			map[string]any{"me": true, "paid": 100},
			map[string]any{"contact_id": f.contactB},
		}}}, "", 201), "paper")
	id := str(paper, "id")
	out := B.do("POST", "/split-bills/"+id+"/decline", nil, "declineSplitBill", 200)
	if out["paper"] != nil {
		t.Errorf("Bee still sees the paper after declining")
	}
	B.fail("GET", "/split-bills/"+id, nil, 404, "BILL_NOT_FOUND")
	view := A.do("PATCH", "/split-bills/"+id+"/people/"+str(person(paper, "Bee"), "id"), map[string]any{"paid_back": true},
		"markSplitBillPerson", 200)
	bee := person(obj(view, "paper"), "Bee")
	if str(bee, "status") != "declined" || bee["paid_back"] != true || str(bee, "paid_back_by") != "Alice" {
		t.Errorf("Bee after decline + mark: %v", bee)
	}
	if v := f.personNet(f.a, "Bee"); !near(v, 50) {
		t.Errorf("the mark moved money: Bee owes %v, want 50", v)
	}
}

// The tx edit's "แบ่ง" switch (owner 2026-11-03): A's plain 300 becomes A's book of a new split bill
// (no second row; the wallet stays as it was); switching it off again (A, the last user, leaves
// keeping the books) deletes the paper and leaves the tx plain.
func TestSplitBillFromMyTx(t *testing.T) {
	f := newFx(t)
	A, B := f.as(f.a), f.as(f.b)
	txID := f.id()
	f.exec(`INSERT INTO transactions (id, user_id, account_id, type, amount, date, category_id, description)
		VALUES ($1, $2, $3, 'expense', 300, '2026-11-01', $4, 'Lunch')`, txID, f.a, f.walletA, f.catA)
	f.exec(`UPDATE accounts SET balance = balance - 300 WHERE id = $1`, f.walletA)

	created := A.do("POST", "/split-bills", map[string]any{
		"paper": map[string]any{"type": "expense", "amount": 300, "date": "2026-11-01", "description": "Lunch",
			"split_mode": "equal", "people": []any{
				map[string]any{"me": true, "paid": 300},
				map[string]any{"person_name": "Dee"},
			}},
		"my_books": map[string]any{"pay": "debt", "transaction_id": txID},
	}, "createSplitBill", 201)
	id := str(obj(created, "paper"), "id")
	if got := str(obj(created, "paper", "my_books", "transaction"), "id"); got != txID.String() {
		t.Errorf("my book = %s, want the existing tx %s", got, txID)
	}
	if n := f.count(`SELECT COUNT(*) FROM transactions WHERE user_id = $1`, f.a); n != 1 {
		t.Errorf("A has %d rows, want 1 (no second tx)", n)
	}
	if b := f.balance(f.walletA); !near(b, 700) {
		t.Errorf("A's wallet %v, want 700 (unchanged)", b)
	}
	if v := f.personNet(f.a, "Dee"); !near(v, 150) {
		t.Errorf("Dee owes %v, want 150", v)
	}
	// Not mine / already in a bill → 400.
	B.fail("POST", "/split-bills", map[string]any{"paper": map[string]any{"type": "expense", "amount": 10,
		"date": "2026-11-01", "people": []any{map[string]any{"me": true, "paid": 10}}},
		"my_books": map[string]any{"transaction_id": txID}}, 400, "VALIDATION_ERROR")
	A.fail("POST", "/split-bills", map[string]any{"paper": map[string]any{"type": "expense", "amount": 10,
		"date": "2026-11-01", "people": []any{map[string]any{"me": true, "paid": 10}, map[string]any{"person_name": "Eve"}}},
		"my_books": map[string]any{"transaction_id": txID}}, 400, "VALIDATION_ERROR")

	// Switched off: the last user leaves keeping the books → the paper goes, the tx stays plain.
	left := A.do("POST", "/split-bills/"+id+"/leave", map[string]any{"books": "keep"}, "leaveSplitBill", 200)
	if !left["deleted"].(bool) {
		t.Errorf("the last leaver did not delete the paper")
	}
	if f.count(`SELECT COUNT(*) FROM transactions WHERE id = $1 AND split_transaction_id IS NULL`, txID) != 1 {
		t.Errorf("the tx is gone or still on a paper")
	}
}
